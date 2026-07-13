package ohlcv

import (
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

func testCandle(timestamp time.Time, open, high, low, close, volume string) contracts.Candle {
	return contracts.Candle{
		TS: timestamp, Open: contracts.MustDecimal(open), High: contracts.MustDecimal(high),
		Low: contracts.MustDecimal(low), Close: contracts.MustDecimal(close),
		Volume: contracts.MustDecimal(volume),
	}
}

func TestValidatedClosedCandlesRejectsFormingMalformedAndDuplicateBars(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	closed := testCandle(now.Add(-2*time.Hour), "100", "110", "90", "105", "12")
	replacement := testCandle(closed.TS, "100", "111", "90", "106", "13")
	forming := testCandle(now.Truncate(time.Hour), "105", "112", "104", "110", "5")
	malformed := testCandle(now.Add(-3*time.Hour), "100", "95", "90", "101", "2")

	result, err := ValidatedClosedCandles("1h", []contracts.Candle{
		forming, closed, malformed, replacement,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Close.Cmp(contracts.MustDecimal("106")) != 0 {
		t.Fatalf("validated candles = %#v", result)
	}
}

func TestMissingTimeRangesFindsLeadingInteriorAndTrailingGaps(t *testing.T) {
	start := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	candles := []contracts.Candle{
		{TS: start.Add(time.Hour)},
		{TS: start.Add(3 * time.Hour)},
	}
	ranges := missingTimeRanges(candles, start, start.Add(5*time.Hour), time.Hour)
	if len(ranges) != 3 || !ranges[0].start.Equal(start) ||
		!ranges[0].end.Equal(start.Add(time.Hour)) ||
		!ranges[1].start.Equal(start.Add(2*time.Hour)) ||
		!ranges[1].end.Equal(start.Add(3*time.Hour)) ||
		!ranges[2].start.Equal(start.Add(4*time.Hour)) ||
		!ranges[2].end.Equal(start.Add(5*time.Hour)) {
		t.Fatalf("missing ranges = %#v", ranges)
	}
}

func TestTimeframeDurationRejectsUnknownInterval(t *testing.T) {
	if duration, err := TimeframeDuration("4h"); err != nil || duration != 4*time.Hour {
		t.Fatalf("duration=%s err=%v", duration, err)
	}
	if _, err := TimeframeDuration("2h"); err == nil {
		t.Fatal("unknown timeframe should fail closed")
	}
}
