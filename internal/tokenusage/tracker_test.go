package tokenusage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/llm"
)

func TestTrackerReportsMultipleProvidersAndAttribution(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tracker, err := New(context.Background(), Config{
		TokenBudget: 1000, CostBudgetUSD: 10, Retention: 90 * 24 * time.Hour,
		Location: time.UTC, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close(context.Background())

	tracker.Record(context.Background(), llm.UsageEvent{
		ID: "anthropic-1", TS: now.Add(-time.Hour), RunID: "run-a", Symbol: "BTC/USDT",
		Agent: "strategist", Source: "automatic", Provider: "anthropic", Model: "claude-a",
		Operation: "text", Status: "success", InputTokens: 30, OutputTokens: 10, CostUSD: 0.04,
	})
	tracker.Record(context.Background(), llm.UsageEvent{
		ID: "deepseek-1", TS: now, RunID: "run-b", Symbol: "ETH/USDT",
		Agent: "risk_officer", Source: "manual", Provider: "deepseek", Model: "deepseek-chat",
		Operation: "json", Status: "success", InputTokens: 20, OutputTokens: 10, CostUSD: 0.02,
	})

	report := tracker.Report(7, "hour", 10)
	if report.Today.Calls != 2 || report.Today.TotalTokens != 70 || report.Today.SuccessRate != 1 {
		t.Fatalf("today = %+v", report.Today)
	}
	if len(report.ByProvider) != 2 || report.ByProvider[0].Key != "anthropic" || report.ByProvider[1].Key != "deepseek" {
		t.Fatalf("providers = %+v", report.ByProvider)
	}
	if len(report.ByAgent) != 2 || len(report.BySource) != 2 || len(report.Recent) != 2 {
		t.Fatalf("attribution missing: agents=%+v sources=%+v recent=%+v", report.ByAgent, report.BySource, report.Recent)
	}
}

func TestTrackerStrictDailyBudgetPausesOnlyNewLLMCalls(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	alerts := make([]BudgetAlert, 0, 2)
	tracker, err := New(context.Background(), Config{
		TokenBudget: 100, CostBudgetUSD: 10, Retention: 24 * time.Hour,
		Location: time.UTC, Now: func() time.Time { return now },
		OnAlert: func(alert BudgetAlert) { alerts = append(alerts, alert) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close(context.Background())

	tracker.Record(context.Background(), llm.UsageEvent{
		ID: "used", TS: now, Provider: "anthropic", Model: "claude", Status: "success",
		InputTokens: 60, OutputTokens: 10,
	})
	if len(alerts) != 1 || alerts[0].Level != "warning" {
		t.Fatalf("70%% alert = %+v", alerts)
	}
	if err := tracker.Reserve(context.Background(), llm.UsageEvent{ID: "next", InputTokens: 31}); !errors.Is(err, llm.ErrDailyBudgetExceeded) {
		t.Fatalf("reserve error = %v", err)
	}
	if len(alerts) != 2 || alerts[1].Level != "paused" || alerts[1].Ratio < 1 {
		t.Fatalf("pause alert = %+v", alerts)
	}
	if snapshot := tracker.Snapshot(); !snapshot.Paused || snapshot.Level != "paused" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestTrackerDoesNotCarryInflightReservationIntoNextNaturalDay(t *testing.T) {
	now := time.Date(2026, 7, 14, 23, 59, 0, 0, time.UTC)
	tracker, err := New(context.Background(), Config{
		TokenBudget: 100, CostBudgetUSD: 10, Retention: 48 * time.Hour,
		Location: time.UTC, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close(context.Background())
	if err := tracker.Reserve(context.Background(), llm.UsageEvent{ID: "day-one", InputTokens: 90}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := tracker.Reserve(context.Background(), llm.UsageEvent{ID: "day-two", InputTokens: 90}); err != nil {
		t.Fatalf("next-day reserve inherited prior-day tokens: %v", err)
	}
}
