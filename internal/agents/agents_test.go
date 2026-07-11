package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type fakeLLM struct {
	enabled   bool
	text      string
	jsonValue json.RawMessage
	err       error
	textCalls int
	jsonCalls int
}

func (fake *fakeLLM) Enabled() bool { return fake != nil && fake.enabled }
func (fake *fakeLLM) Text(context.Context, string, string, bool) (string, error) {
	fake.textCalls++
	return fake.text, fake.err
}
func (fake *fakeLLM) JSON(_ context.Context, _, _ string, _ json.RawMessage, out any, _ bool) error {
	fake.jsonCalls++
	if fake.err != nil {
		return fake.err
	}
	return json.Unmarshal(fake.jsonValue, out)
}

func testCandles(count int, start, step float64) contracts.List[contracts.Candle] {
	candles := make(contracts.List[contracts.Candle], 0, count)
	for index := 0; index < count; index++ {
		closeValue := start + float64(index)*step
		close, _ := decimalFromFloat(closeValue)
		high, _ := decimalFromFloat(closeValue + 2)
		low, _ := decimalFromFloat(closeValue - 2)
		open, _ := decimalFromFloat(closeValue - step/2)
		candles = append(candles, contracts.Candle{
			TS: time.Unix(int64(index*3600), 0).UTC(), Open: open, High: high, Low: low,
			Close: close, Volume: contracts.MustDecimal("100"),
		})
	}
	return candles
}

func testSnapshot(symbol string) contracts.MarketSnapshot {
	return contracts.MarketSnapshot{
		Symbol: symbol, Venue: "paper", TS: time.Now().UTC(), OHLCV: testCandles(80, 100, 1),
	}
}

func bullishReports() []contracts.AnalystReport {
	return []contracts.AnalystReport{
		{Agent: contracts.AgentTechnical, Stance: contracts.StanceBullish, Confidence: 0.9,
			Signals: contracts.List[contracts.Signal]{}, Rationale: "trend"},
		{Agent: contracts.AgentDerivatives, Stance: contracts.StanceBullish, Confidence: 0.7,
			Signals: contracts.List[contracts.Signal]{}, Rationale: "funding"},
	}
}

func TestBlendAndStanceSign(t *testing.T) {
	t.Parallel()
	stance, confidence := Blend([]Vote{{Sign: 1, Weight: 1}, {Sign: -1, Weight: 0.2}})
	if stance != contracts.StanceBullish || confidence <= 0 || confidence > 1 {
		t.Fatalf("blend = %s %.3f", stance, confidence)
	}
	stance, confidence = Blend(nil)
	if stance != contracts.StanceNeutral || confidence != 0.2 || StanceSign(contracts.StanceBearish) != -1 {
		t.Fatalf("neutral blend/sign mismatch: %s %.2f", stance, confidence)
	}
}

func TestSensitiveTextRedaction(t *testing.T) {
	t.Parallel()
	input := "api_key=secret-value Authorization:Bearer abcdefghijkl sk-abcdefghijklmnop -----BEGIN PRIVATE KEY-----\nabc123\n-----END PRIVATE KEY-----"
	redacted := redactSensitive(input)
	for _, secret := range []string{"secret-value", "abcdefghijkl", "abc123"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redaction leaked %q in %q", secret, redacted)
		}
	}
}

func TestAnalystsRuleOutputsAndMissingDataDegrade(t *testing.T) {
	t.Parallel()
	technical, err := (TechnicalAnalyst{}).Run(context.Background(), testSnapshot("BTC/USDT"), AgentContext{})
	if err != nil || technical.Degraded || len(technical.Signals) == 0 {
		t.Fatalf("technical = %+v, %v", technical, err)
	}
	empty, err := (TechnicalAnalyst{}).Run(context.Background(), contracts.MarketSnapshot{}, AgentContext{})
	if err != nil || !empty.Degraded || empty.Confidence != 0.2 {
		t.Fatalf("empty technical = %+v, %v", empty, err)
	}

	funding := contracts.MustDecimal("0.001")
	ratio := contracts.MustDecimal("1.20")
	derivatives, err := (DerivativesAnalyst{}).Run(context.Background(), contracts.MarketSnapshot{
		Derivatives: &contracts.DerivativesData{FundingRate: &funding, LongShortRatio: &ratio},
	}, AgentContext{})
	if err != nil || derivatives.Stance != contracts.StanceBearish || derivatives.Degraded {
		t.Fatalf("derivatives = %+v, %v", derivatives, err)
	}

	fearGreed := 10
	news := contracts.MustDecimal("0.5")
	sentiment, err := (SentimentAnalyst{}).Run(context.Background(), contracts.MarketSnapshot{
		Sentiment: &contracts.SentimentData{FearGreed: &fearGreed, NewsScore: &news},
	}, AgentContext{})
	if err != nil || sentiment.Stance != contracts.StanceBullish {
		t.Fatalf("sentiment = %+v, %v", sentiment, err)
	}

	flow := contracts.MustDecimal("1000")
	netflow := contracts.MustDecimal("-10")
	onchain, err := (OnchainAnalyst{}).Run(context.Background(), contracts.MarketSnapshot{
		Onchain: &contracts.OnchainData{SmartMoneyFlow: &flow, ExchangeNetflow: &netflow},
	}, AgentContext{})
	if err != nil || onchain.Stance != contracts.StanceBullish {
		t.Fatalf("onchain = %+v, %v", onchain, err)
	}
}

type failingAnalyst struct{ id contracts.AgentID }

func (analyst failingAnalyst) ID() contracts.AgentID { return analyst.id }
func (failingAnalyst) Run(context.Context, contracts.MarketSnapshot, AgentContext) (contracts.AnalystReport, error) {
	return contracts.AnalystReport{}, errors.New("secret upstream detail")
}

func TestRunAnalystsPreservesOrderAndIsolatesFailure(t *testing.T) {
	t.Parallel()
	panel := []Analyst{failingAnalyst{id: contracts.AgentTechnical}, DerivativesAnalyst{}}
	reports, err := RunAnalysts(context.Background(), panel, contracts.MarketSnapshot{}, AgentContext{})
	if err != nil || len(reports) != 2 {
		t.Fatalf("reports=%+v err=%v", reports, err)
	}
	if reports[0].Agent != contracts.AgentTechnical || !reports[0].Degraded ||
		reports[1].Agent != contracts.AgentDerivatives || !reports[1].Degraded {
		t.Fatalf("ordered reports = %+v", reports)
	}
	if strings.Contains(reports[0].Rationale, "secret upstream detail") {
		t.Fatal("failure isolation leaked raw upstream error")
	}
}

func TestStrategistDeterministicParametersAndTextOnlyLLM(t *testing.T) {
	t.Parallel()
	strategist := NewStrategist(nil)
	snapshot := testSnapshot("BTC/USDT")
	riskConfig := config.DefaultRiskConfig()
	withoutLLM, err := strategist.Run(context.Background(), bullishReports(), snapshot,
		contracts.MustDecimal("10000"), riskConfig, AgentContext{}, "paper", nil)
	if err != nil {
		t.Fatal(err)
	}
	if withoutLLM.Side != contracts.SideLong || withoutLLM.Instrument != contracts.InstrumentSpot ||
		withoutLLM.StopLoss == nil || withoutLLM.StopLoss.Cmp(snapshot.OHLCV[len(snapshot.OHLCV)-1].Close) >= 0 ||
		len(withoutLLM.TakeProfit) != 1 || !withoutLLM.SizeQuote.IsPositive() {
		t.Fatalf("proposal = %+v", withoutLLM)
	}
	if withoutLLM.SizeQuote.Cmp(contracts.MustDecimal("2000")) > 0 {
		t.Fatalf("position cap exceeded: %s", withoutLLM.SizeQuote)
	}

	fake := &fakeLLM{enabled: true, text: "LLM 仅润色该规则提案。"}
	withLLM, err := strategist.Run(context.Background(), bullishReports(), snapshot,
		contracts.MustDecimal("10000"), riskConfig, AgentContext{LLM: fake}, "paper", nil)
	if err != nil {
		t.Fatal(err)
	}
	if withLLM.Thesis != fake.text || fake.textCalls != 1 {
		t.Fatalf("LLM thesis/calls = %q/%d", withLLM.Thesis, fake.textCalls)
	}
	if withLLM.SizeQuote.Cmp(withoutLLM.SizeQuote) != 0 || withLLM.Side != withoutLLM.Side ||
		withLLM.StopLoss.Cmp(*withoutLLM.StopLoss) != 0 {
		t.Fatal("text LLM altered deterministic trade parameters")
	}
}

func TestStrategistSafetyFallbacks(t *testing.T) {
	t.Parallel()
	strategist := NewStrategist(nil)
	riskConfig := config.DefaultRiskConfig()
	snapshot := testSnapshot("BTC/USDT")
	bearish := bullishReports()
	for index := range bearish {
		bearish[index].Stance = contracts.StanceBearish
	}
	proposal, err := strategist.Run(context.Background(), bearish, snapshot,
		contracts.MustDecimal("10000"), riskConfig, AgentContext{}, "paper", nil)
	if err != nil || proposal.Side != contracts.SideFlat {
		t.Fatalf("spot short fallback = %+v, %v", proposal, err)
	}

	perpetual := testSnapshot("BTC/USDT:USDT")
	proposal, err = strategist.Run(context.Background(), bullishReports(), perpetual,
		contracts.MustDecimal("10000"), riskConfig, AgentContext{AllowPerp: false}, "paper", nil)
	if err != nil || proposal.Side != contracts.SideFlat {
		t.Fatalf("disabled perp fallback = %+v, %v", proposal, err)
	}

	last := snapshot.OHLCV[len(snapshot.OHLCV)-1].Close
	existing := []contracts.Position{{
		Symbol: snapshot.Symbol, Venue: "paper", Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		SizeBase: contracts.MustDecimal("1"), EntryPrice: last, Leverage: 1,
	}}
	proposal, err = strategist.Run(context.Background(), bullishReports(), snapshot,
		contracts.MustDecimal("10000"), riskConfig, AgentContext{}, "paper", existing)
	if err != nil || proposal.Side != contracts.SideFlat || !strings.Contains(proposal.Thesis, "不加仓") {
		t.Fatalf("same-direction fallback = %+v, %v", proposal, err)
	}
}

func TestRiskOfficerCanOnlyTightenAndFallsBackOnBadJSON(t *testing.T) {
	t.Parallel()
	proposal := contracts.TradeProposal{
		Symbol: "BTC/USDT", Venue: "paper", Side: contracts.SideLong, Instrument: contracts.InstrumentSpot,
		SizeQuote: contracts.MustDecimal("100"), Leverage: 1, MarginMode: contracts.MarginModeIsolated,
		Entry: contracts.PricePlan{Type: contracts.EntryTypeMarket}, Confidence: 0.8,
	}
	fake := &fakeLLM{enabled: true, jsonValue: json.RawMessage(`{"risk_score":0.8,"escalate_reject":true,"notes":"事件风险"}`)}
	officer := RiskOfficer{}
	assessment := contracts.RiskAssessment{
		Verdict: contracts.VerdictApproved, HardViolations: contracts.List[string]{}, RiskScore: 0.2,
	}
	result, err := officer.Run(context.Background(), proposal, assessment, bullishReports(), AgentContext{LLM: fake})
	if err != nil || result.Verdict != contracts.VerdictRejected || result.RiskScore != 0.8 ||
		!result.LLMReviewed || len(result.HardViolations) != 1 {
		t.Fatalf("tightened result = %+v, %v", result, err)
	}

	hardRejected := assessment
	hardRejected.Verdict = contracts.VerdictRejected
	beforeCalls := fake.jsonCalls
	result, err = officer.Run(context.Background(), proposal, hardRejected, bullishReports(), AgentContext{LLM: fake})
	if err != nil || result.Verdict != contracts.VerdictRejected || fake.jsonCalls != beforeCalls {
		t.Fatal("hard rejection was sent to or revived by LLM")
	}

	invalid := &fakeLLM{enabled: true, jsonValue: json.RawMessage(`{"notes":"missing score"}`)}
	result, err = officer.Run(context.Background(), proposal, assessment, bullishReports(), AgentContext{LLM: invalid})
	if err != nil || result.LLMReviewed || result.Verdict != assessment.Verdict || result.RiskScore != assessment.RiskScore {
		t.Fatalf("invalid structured fallback = %+v, %v", result, err)
	}

	lower := &fakeLLM{enabled: true, jsonValue: json.RawMessage(`{"risk_score":0.1,"escalate_reject":false,"notes":"低估风险"}`)}
	result, err = officer.Run(context.Background(), proposal, assessment, bullishReports(), AgentContext{LLM: lower})
	if err != nil || result.RiskScore != assessment.RiskScore || result.Verdict != contracts.VerdictApproved {
		t.Fatalf("soft reviewer lowered hard-risk result = %+v, %v", result, err)
	}

	failed := &fakeLLM{enabled: true, err: errors.New("provider unavailable")}
	result, err = officer.Run(context.Background(), proposal, assessment, bullishReports(), AgentContext{LLM: failed})
	if err != nil || result.LLMReviewed || result.RiskScore != assessment.RiskScore || result.Verdict != assessment.Verdict {
		t.Fatalf("provider failure did not degrade to hard risk = %+v, %v", result, err)
	}
}

func TestReviewerRuleFallback(t *testing.T) {
	t.Parallel()
	proposal := contracts.TradeProposal{Symbol: "BTC/USDT", Side: contracts.SideLong, Confidence: 0.2}
	slippage := contracts.MustDecimal("25")
	reviewer := Reviewer{Now: func() time.Time { return time.Unix(100, 0) }}
	review, err := reviewer.Run(context.Background(), proposal, contracts.ExecutionResult{
		Status: contracts.OrderStatusFilled, SlippageBPS: &slippage,
	}, AgentContext{}, "run-1")
	if err != nil || math.Abs(review.Score-0.4) > 1e-12 || len(review.Lessons) != 2 || review.ProposalRef != "run-1" || review.Kind != "entry" {
		t.Fatalf("review = %+v, %v", review, err)
	}
	failure := "venue unavailable"
	review, err = reviewer.Run(context.Background(), proposal, contracts.ExecutionResult{
		Status: contracts.OrderStatusFailed, Error: &failure,
	}, AgentContext{}, "run-2")
	if err != nil || review.Score != 0.2 || !strings.Contains(fmt.Sprint(review.Lessons), failure) {
		t.Fatalf("failure review = %+v, %v", review, err)
	}
	price := contracts.MustDecimal("90")
	review, err = reviewer.RunClosed(context.Background(), contracts.Position{
		Symbol: "BTC/USDT", Side: contracts.SideLong,
	}, contracts.ExecutionResult{
		Status: contracts.OrderStatusFilled, AvgPrice: &price, SlippageBPS: &slippage,
	}, contracts.MustDecimal("-11"), "run-1")
	if err != nil || review.Kind != "close" || review.PNLQuote.String() != "-11" || math.Abs(review.Score-0.1) > 1e-12 ||
		!strings.Contains(fmt.Sprint(review.Lessons), "亏损") {
		t.Fatalf("close review = %+v, %v", review, err)
	}
}
