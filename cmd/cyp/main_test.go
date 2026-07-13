package main

import (
	"strings"
	"testing"
)

func TestParseFloatList(t *testing.T) {
	values, err := parseFloatList("0.1, 0.2")
	if err != nil || len(values) != 2 || values[1] != 0.2 {
		t.Fatalf("parseFloatList() = %#v, %v", values, err)
	}
	if _, err := parseFloatList(" "); err == nil {
		t.Fatal("empty list unexpectedly succeeded")
	}
}

func TestBacktestSyntheticRunsOffline(t *testing.T) {
	if err := runBacktest([]string{"-bars", "120", "-window", "30", "-json"}); err != nil {
		t.Fatalf("synthetic backtest failed: %v", err)
	}
}

func TestBacktestCEXRejectsUnknownExchange(t *testing.T) {
	err := runBacktest([]string{"-data", "cex", "-cex", "bogus"})
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown exchange error = %v", err)
	}
}
