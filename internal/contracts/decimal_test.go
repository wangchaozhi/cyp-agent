package contracts

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseDecimalExactRepresentations(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"0":          "0",
		"-0.00":      "-0.00",
		"001.2300":   "1.2300",
		".5":         "0.5",
		"1.":         "1",
		"1.25e2":     "125",
		"1.2300e2":   "123.00",
		"12e-3":      "0.012",
		"  +42.10  ": "42.10",
	}
	for input, expected := range tests {
		input, expected := input, expected
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			actual, err := ParseDecimal(input)
			if err != nil {
				t.Fatalf("ParseDecimal(%q): %v", input, err)
			}
			if actual.String() != expected {
				t.Fatalf("ParseDecimal(%q) = %q, want %q", input, actual, expected)
			}
		})
	}
}

func TestDashboardEventUsesFlatExtensibleEnvelope(t *testing.T) {
	t.Parallel()
	event := DashboardEvent{
		Type: "run_done", RunID: "abc123", TS: time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
		Data: map[string]any{"symbol": "BTC/USDT", "status": "executed", "type": "cannot-override"},
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, `"data"`) || !strings.Contains(text, `"type":"run_done"`) || !strings.Contains(text, `"symbol":"BTC/USDT"`) {
		t.Fatalf("event is not a flat stable envelope: %s", text)
	}
	var decoded DashboardEvent
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != event.Type || decoded.RunID != event.RunID || decoded.Data["status"] != "executed" {
		t.Fatalf("decoded event = %#v", decoded)
	}
}

func TestDecimalJSONAcceptsStringAndNumberButAlwaysWritesString(t *testing.T) {
	t.Parallel()
	type payload struct {
		Amount Decimal `json:"amount"`
	}
	for _, input := range []string{`{"amount":"123.4500"}`, `{"amount":123.4500}`} {
		var decoded payload
		if err := json.Unmarshal([]byte(input), &decoded); err != nil {
			t.Fatalf("Unmarshal(%s): %v", input, err)
		}
		if got := decoded.Amount.String(); got != "123.4500" {
			t.Fatalf("decoded amount = %q", got)
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{"amount":"123.4500"}` {
			t.Fatalf("encoded = %s", encoded)
		}
	}
}

func TestDecimalRejectsInvalidAndNonFiniteInputs(t *testing.T) {
	t.Parallel()
	invalidText := []string{"", ".", "+", "--1", "1,2", "NaN", "nan", "Infinity", "+Inf", "0x10", "1e10001"}
	for _, input := range invalidText {
		if _, err := ParseDecimal(input); err == nil {
			t.Errorf("ParseDecimal(%q) unexpectedly succeeded", input)
		}
	}
	invalidJSON := []string{`null`, `true`, `[]`, `{}`, `"NaN"`, `"Infinity"`, `1e10001`}
	for _, input := range invalidJSON {
		var value Decimal
		if err := json.Unmarshal([]byte(input), &value); err == nil {
			t.Errorf("json.Unmarshal(%s) unexpectedly succeeded", input)
		}
	}
}

func TestDecimalArithmeticAndComparison(t *testing.T) {
	t.Parallel()
	a := MustDecimal("10.50")
	b := MustDecimal("2.0")
	if got := a.Add(b).String(); got != "12.50" {
		t.Fatalf("Add = %s", got)
	}
	if got := a.Sub(b).String(); got != "8.50" {
		t.Fatalf("Sub = %s", got)
	}
	if got := a.Mul(b).String(); got != "21.000" {
		t.Fatalf("Mul = %s", got)
	}
	if a.Cmp(MustDecimal("10.5000")) != 0 || !a.Equal(MustDecimal("10.5")) {
		t.Fatal("numerically equal decimals must compare equal")
	}
	if !a.Neg().IsNegative() || a.Abs().Cmp(a) != 0 || !Zero().IsZero() {
		t.Fatal("sign helpers returned inconsistent results")
	}
}

func TestDecimalDivisionRounding(t *testing.T) {
	t.Parallel()
	half, err := MustDecimal("1").Quo(MustDecimal("2"))
	if err != nil || half.String() != "0.5" {
		t.Fatalf("1/2 = %s, %v", half, err)
	}
	tests := []struct {
		numerator string
		mode      RoundingMode
		want      string
	}{
		{"1", RoundDown, "0.12"},
		{"1", RoundHalfUp, "0.13"},
		{"1", RoundHalfEven, "0.12"},
		{"3", RoundHalfEven, "0.38"},
		{"-1", RoundHalfUp, "-0.13"},
	}
	for _, test := range tests {
		got, err := MustDecimal(test.numerator).QuoScale(MustDecimal("8"), 2, test.mode)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != test.want {
			t.Errorf("%s/8 mode %d = %s, want %s", test.numerator, test.mode, got, test.want)
		}
	}
	if _, err := MustDecimal("1").Quo(Zero()); err == nil {
		t.Fatal("division by zero unexpectedly succeeded")
	}
}

func TestDecimalFloat64RejectsOverflow(t *testing.T) {
	t.Parallel()
	finite, err := MustDecimal("1.25").Float64()
	if err != nil || finite != 1.25 {
		t.Fatalf("Float64 = %v, %v", finite, err)
	}
	huge := MustDecimal("1e9999")
	if value, err := huge.Float64(); err == nil || math.IsInf(value, 0) {
		t.Fatalf("overflow conversion must return an error and finite zero, got %v, %v", value, err)
	}
}

func TestListUsesArrayRatherThanNull(t *testing.T) {
	t.Parallel()
	var items List[string]
	encoded, err := json.Marshal(items)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("nil List encoded as %s, %v", encoded, err)
	}
	if err := json.Unmarshal([]byte("null"), &items); err == nil {
		t.Fatal("null list unexpectedly accepted")
	}
}

func TestTradeProposalJSONAndValidation(t *testing.T) {
	t.Parallel()
	proposal := TradeProposal{
		Symbol: "BTC/USDT", Venue: "paper", Side: SideLong, Instrument: InstrumentSpot,
		SizeQuote: MustDecimal("100.00"), Leverage: 1, MarginMode: MarginModeIsolated,
		Entry: PricePlan{Type: EntryTypeMarket}, Confidence: 0.8,
	}
	if err := proposal.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, fragment := range []string{
		`"size_quote":"100.00"`, `"stop_loss":null`, `"price":null`,
		`"take_profit":[]`, `"supporting_reports":[]`,
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("proposal JSON %s missing %s", text, fragment)
		}
	}
	proposal.Leverage = math.Inf(1)
	if err := proposal.Validate(); err == nil {
		t.Fatal("non-finite leverage unexpectedly accepted")
	}
}
