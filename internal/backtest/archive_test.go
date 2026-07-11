package backtest

import (
	"testing"
	"time"
)

func TestTimeframeDuration(t *testing.T) {
	if value, err := TimeframeDuration("4h"); err != nil || value != 4*time.Hour {
		t.Fatalf("TimeframeDuration(4h) = %v, %v", value, err)
	}
	if _, err := TimeframeDuration("2h"); err == nil {
		t.Fatal("unsupported timeframe unexpectedly succeeded")
	}
}
