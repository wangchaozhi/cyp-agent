package tokenusage

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/llm"
	"github.com/wangchaozhi/cyp-agent/internal/observability"
)

type Config struct {
	Store         Store
	TokenBudget   int
	CostBudgetUSD float64
	Retention     time.Duration
	Location      *time.Location
	QueueSize     int
	Logger        *slog.Logger
	OnAlert       func(BudgetAlert)
	Now           func() time.Time
	Metrics       *observability.RuntimeMetrics
}

type Tracker struct {
	store         Store
	tokenBudget   int
	costBudgetUSD float64
	retention     time.Duration
	location      *time.Location
	logger        *slog.Logger
	onAlert       func(BudgetAlert)
	now           func() time.Time
	metrics       *observability.RuntimeMetrics
	queue         chan llm.UsageEvent
	cancel        context.CancelFunc
	done          chan struct{}
	closing       atomic.Bool
	closeOnce     sync.Once

	mu           sync.RWMutex
	events       []llm.UsageEvent
	daily        map[string]usageTotals
	reservations map[string]reservation
	alerted      map[string]map[string]bool
	pausedDay    string
	nextPrune    time.Time
}

type reservation struct {
	day    string
	tokens int
	cost   float64
}

type usageTotals struct {
	calls            int
	successes        int
	errors           int
	budgetRejections int
	inputTokens      int
	outputTokens     int
	costUSD          float64
}

func New(ctx context.Context, config Config) (*Tracker, error) {
	if config.TokenBudget <= 0 || config.CostBudgetUSD <= 0 {
		return nil, errors.New("daily token and cost budgets must be positive")
	}
	if config.Retention <= 0 {
		return nil, errors.New("token usage retention must be positive")
	}
	if config.Location == nil {
		config.Location = time.UTC
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 512
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tracker := &Tracker{
		store: config.Store, tokenBudget: config.TokenBudget, costBudgetUSD: config.CostBudgetUSD,
		retention: config.Retention, location: config.Location, logger: config.Logger,
		onAlert: config.OnAlert, now: config.Now, metrics: config.Metrics,
		queue: make(chan llm.UsageEvent, config.QueueSize),
		done:  make(chan struct{}), daily: make(map[string]usageTotals),
		reservations: make(map[string]reservation), alerted: make(map[string]map[string]bool),
		nextPrune: config.Now().UTC().Add(time.Hour),
	}
	if config.Store != nil {
		loaded, err := config.Store.Load(ctx, config.Now().Add(-config.Retention))
		if err != nil {
			return nil, err
		}
		tracker.events = validatedEvents(loaded)
		tracker.rebuildDailyLocked()
	}
	workerContext, cancel := context.WithCancel(context.Background())
	tracker.cancel = cancel
	go tracker.run(workerContext)
	return tracker, nil
}

func (tracker *Tracker) Reserve(_ context.Context, event llm.UsageEvent) error {
	if tracker == nil || tracker.closing.Load() {
		return llm.ErrDailyBudgetExceeded
	}
	now := tracker.now().UTC()
	day := tracker.dayKey(now)
	tracker.mu.Lock()
	if tracker.pausedDay != "" && tracker.pausedDay != day {
		tracker.pausedDay = ""
	}
	for id, current := range tracker.reservations {
		if current.day != day {
			delete(tracker.reservations, id)
		}
	}
	summary := tracker.summaryLocked(day)
	predictedEventCost := event.CostUSD
	if math.IsNaN(predictedEventCost) || math.IsInf(predictedEventCost, 0) || predictedEventCost < 0 {
		predictedEventCost = 0
	}
	reserved := 0
	reservedCost := 0.0
	for _, current := range tracker.reservations {
		if current.day == day {
			reserved += current.tokens
			reservedCost += current.cost
		}
	}
	predictedTokens := summary.TotalTokens + reserved + maxInt(event.TotalTokens(), 0)
	predictedCost := summary.CostUSD + reservedCost + predictedEventCost
	blocked := tracker.pausedDay == day || summary.Utilization >= 1 ||
		predictedTokens > tracker.tokenBudget || predictedCost > tracker.costBudgetUSD
	if blocked {
		tracker.pausedDay = day
		alert := tracker.pausedAlertLocked(day, summary)
		tracker.mu.Unlock()
		if alert != nil && tracker.onAlert != nil {
			tracker.onAlert(*alert)
		}
		return llm.ErrDailyBudgetExceeded
	}
	tracker.reservations[event.ID] = reservation{
		day: day, tokens: maxInt(event.TotalTokens(), 0), cost: predictedEventCost,
	}
	tracker.mu.Unlock()
	return nil
}

func (tracker *Tracker) Record(_ context.Context, event llm.UsageEvent) {
	if tracker == nil || tracker.closing.Load() {
		return
	}
	if event.TS.IsZero() {
		event.TS = tracker.now().UTC()
	} else {
		event.TS = event.TS.UTC()
	}
	event.Provider = normalized(event.Provider, "unknown")
	event.Model = normalized(event.Model, "unknown")
	event.Agent = normalized(event.Agent, "unattributed")
	event.Symbol = normalized(event.Symbol, "unattributed")
	event.Source = normalized(event.Source, "unknown")
	event.Operation = normalized(event.Operation, "unknown")
	event.Status = normalized(event.Status, "error")
	event.InputTokens = maxInt(event.InputTokens, 0)
	event.OutputTokens = maxInt(event.OutputTokens, 0)
	if math.IsNaN(event.CostUSD) || math.IsInf(event.CostUSD, 0) || event.CostUSD < 0 {
		event.CostUSD = 0
	}

	tracker.mu.Lock()
	delete(tracker.reservations, event.ID)
	tracker.insertEventLocked(event)
	tracker.addDailyLocked(event)
	now := tracker.now().UTC()
	if !now.Before(tracker.nextPrune) {
		tracker.pruneMemoryLocked(now.Add(-tracker.retention))
		tracker.nextPrune = now.Add(time.Hour)
	}
	day := tracker.dayKey(event.TS)
	summary := tracker.summaryLocked(day)
	alerts := tracker.newAlertsLocked(day, summary)
	tracker.mu.Unlock()

	if tracker.store != nil {
		select {
		case tracker.queue <- event:
			tracker.metrics.RecordTokenUsageQueued()
		default:
			tracker.metrics.RecordTokenUsageDropped()
			tracker.logger.Warn("token_usage_queue_full", "provider", event.Provider, "model", event.Model)
		}
	}
	for _, alert := range alerts {
		if tracker.onAlert != nil {
			tracker.onAlert(alert)
		}
	}
}

func (tracker *Tracker) Snapshot() Summary {
	if tracker == nil {
		return Summary{}
	}
	day := tracker.dayKey(tracker.now())
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	return tracker.summaryLocked(day)
}

func (tracker *Tracker) Report(days int, bucket string, recentLimit int) Report {
	if tracker == nil {
		return EmptyReport()
	}
	maxDays := maxInt(1, int(tracker.retention/(24*time.Hour)))
	if days < 1 {
		days = 7
	}
	if days > maxDays {
		days = maxDays
	}
	if bucket != "hour" && bucket != "day" {
		if days <= 7 {
			bucket = "hour"
		} else {
			bucket = "day"
		}
	}
	if recentLimit <= 0 || recentLimit > 200 {
		recentLimit = 50
	}
	now := tracker.now().UTC()
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	tracker.mu.RLock()
	start := sort.Search(len(tracker.events), func(index int) bool {
		return !tracker.events[index].TS.Before(since)
	})
	filtered := append([]llm.UsageEvent(nil), tracker.events[start:]...)
	today := tracker.summaryLocked(tracker.dayKey(now))
	tracker.mu.RUnlock()
	return Report{
		GeneratedAt: now, Days: days, Bucket: bucket, Today: today,
		Trend:      tracker.trend(filtered, bucket),
		ByProvider: dimensions(filtered, func(event llm.UsageEvent) string { return event.Provider }),
		ByModel:    dimensions(filtered, func(event llm.UsageEvent) string { return event.Model }),
		ByAgent:    dimensions(filtered, func(event llm.UsageEvent) string { return event.Agent }),
		BySymbol:   dimensions(filtered, func(event llm.UsageEvent) string { return event.Symbol }),
		BySource:   dimensions(filtered, func(event llm.UsageEvent) string { return event.Source }),
		Recent:     recentEvents(filtered, recentLimit),
	}
}

func (tracker *Tracker) summaryLocked(day string) Summary {
	totals := tracker.daily[day]
	summary := Summary{
		Day: day, Timezone: tracker.location.String(), TokenBudget: tracker.tokenBudget,
		CostBudgetUSD: tracker.costBudgetUSD, Calls: totals.calls, Successes: totals.successes,
		Errors: totals.errors, BudgetRejections: totals.budgetRejections,
		InputTokens: totals.inputTokens, OutputTokens: totals.outputTokens, CostUSD: totals.costUSD,
	}
	summary.TotalTokens = summary.InputTokens + summary.OutputTokens
	if summary.Calls > 0 {
		summary.SuccessRate = float64(summary.Successes) / float64(summary.Calls)
	}
	summary.TokenRatio = float64(summary.TotalTokens) / float64(tracker.tokenBudget)
	summary.CostRatio = summary.CostUSD / tracker.costBudgetUSD
	summary.Utilization = math.Max(summary.TokenRatio, summary.CostRatio)
	summary.Level = usageLevel(summary.Utilization)
	summary.Paused = tracker.pausedDay == day || summary.Utilization >= 1
	if summary.Paused {
		summary.Level = "paused"
	}
	return summary
}

func (tracker *Tracker) pausedAlertLocked(day string, summary Summary) *BudgetAlert {
	if tracker.alerted[day] == nil {
		tracker.alerted[day] = make(map[string]bool)
	}
	if tracker.alerted[day]["paused"] {
		return nil
	}
	tracker.alerted[day]["paused"] = true
	summary.Paused = true
	summary.Level = "paused"
	return &BudgetAlert{Level: "paused", Ratio: math.Max(1, summary.Utilization), Summary: summary}
}

func (tracker *Tracker) newAlertsLocked(day string, summary Summary) []BudgetAlert {
	if tracker.alerted[day] == nil {
		tracker.alerted[day] = make(map[string]bool)
	}
	levels := []struct {
		name  string
		ratio float64
	}{{"warning", 0.70}, {"critical", 0.90}, {"paused", 1.0}}
	alerts := make([]BudgetAlert, 0, 1)
	for _, level := range levels {
		if summary.Utilization >= level.ratio && !tracker.alerted[day][level.name] {
			tracker.alerted[day][level.name] = true
			if level.name == "paused" {
				tracker.pausedDay = day
				summary.Paused = true
			}
			alerts = append(alerts, BudgetAlert{Level: level.name, Ratio: summary.Utilization, Summary: summary})
		}
	}
	return alerts
}

func (tracker *Tracker) trend(events []llm.UsageEvent, bucket string) []TrendBucket {
	result := make(map[time.Time]*TrendBucket)
	for _, event := range events {
		local := event.TS.In(tracker.location)
		var start time.Time
		if bucket == "day" {
			start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, tracker.location)
		} else {
			start = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, tracker.location)
		}
		item := result[start]
		if item == nil {
			item = &TrendBucket{Start: start}
			result[start] = item
		}
		item.Calls++
		if event.Status == "success" {
			item.Successes++
		}
		item.InputTokens += event.InputTokens
		item.OutputTokens += event.OutputTokens
		item.TotalTokens += event.TotalTokens()
		item.CostUSD += event.CostUSD
	}
	keys := make([]time.Time, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	buckets := make([]TrendBucket, 0, len(keys))
	for _, key := range keys {
		buckets = append(buckets, *result[key])
	}
	return buckets
}

func dimensions(events []llm.UsageEvent, key func(llm.UsageEvent) string) []Dimension {
	values := make(map[string]*Dimension)
	for _, event := range events {
		name := normalized(key(event), "unattributed")
		item := values[name]
		if item == nil {
			item = &Dimension{Key: name}
			values[name] = item
		}
		item.Calls++
		if event.Status == "success" {
			item.Successes++
		}
		item.InputTokens += event.InputTokens
		item.OutputTokens += event.OutputTokens
		item.TotalTokens += event.TotalTokens()
		item.CostUSD += event.CostUSD
	}
	result := make([]Dimension, 0, len(values))
	for _, value := range values {
		result = append(result, *value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalTokens == result[j].TotalTokens {
			return result[i].Key < result[j].Key
		}
		return result[i].TotalTokens > result[j].TotalTokens
	})
	if len(result) > 12 {
		result = result[:12]
	}
	return result
}

func recentEvents(events []llm.UsageEvent, limit int) []llm.UsageEvent {
	if len(events) == 0 {
		return []llm.UsageEvent{}
	}
	start := len(events) - limit
	if start < 0 {
		start = 0
	}
	result := append([]llm.UsageEvent(nil), events[start:]...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func (tracker *Tracker) run(ctx context.Context) {
	defer close(tracker.done)
	if tracker.store == nil {
		<-ctx.Done()
		return
	}
	pruneTicker := time.NewTicker(24 * time.Hour)
	defer pruneTicker.Stop()
	tracker.pruneStore()
	for {
		select {
		case event := <-tracker.queue:
			writeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := tracker.saveEvent(writeContext, event)
			cancel()
			if err != nil {
				tracker.metrics.RecordTokenUsageError()
				tracker.logger.Error("token_usage_write_failed", "error", err.Error(), "provider", event.Provider, "model", event.Model)
			}
		case <-pruneTicker.C:
			tracker.pruneStore()
		case <-ctx.Done():
			for {
				select {
				case event := <-tracker.queue:
					writeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					err := tracker.saveEvent(writeContext, event)
					cancel()
					if err != nil {
						tracker.metrics.RecordTokenUsageError()
					}
				default:
					return
				}
			}
		}
	}
}

func (tracker *Tracker) saveEvent(ctx context.Context, event llm.UsageEvent) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = tracker.store.Save(ctx, event, tracker.dayKey(event.TS), tracker.location.String())
		if lastErr == nil {
			tracker.metrics.RecordTokenUsageSaved()
			return nil
		}
		if attempt == 2 {
			break
		}
		delay := time.Duration(attempt+1) * 100 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (tracker *Tracker) pruneStore() {
	if tracker.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err := tracker.store.Prune(ctx, tracker.now().Add(-tracker.retention))
	cancel()
	if err != nil {
		tracker.logger.Error("token_usage_prune_failed", "error", err.Error())
	}
}

func (tracker *Tracker) Close(ctx context.Context) error {
	if tracker == nil {
		return nil
	}
	tracker.closeOnce.Do(func() {
		tracker.closing.Store(true)
		tracker.cancel()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-tracker.done:
		if tracker.store != nil {
			tracker.store.Close()
		}
		return nil
	case <-ctx.Done():
		if tracker.store != nil {
			tracker.store.Close()
		}
		return ctx.Err()
	}
}

func (tracker *Tracker) dayKey(timestamp time.Time) string {
	return timestamp.In(tracker.location).Format("2006-01-02")
}

func (tracker *Tracker) pruneMemoryLocked(before time.Time) {
	start := sort.Search(len(tracker.events), func(index int) bool {
		return !tracker.events[index].TS.Before(before)
	})
	if start > 0 {
		copy(tracker.events, tracker.events[start:])
		for index := len(tracker.events) - start; index < len(tracker.events); index++ {
			tracker.events[index] = llm.UsageEvent{}
		}
		tracker.events = tracker.events[:len(tracker.events)-start]
	}
	tracker.rebuildDailyLocked()
}

func (tracker *Tracker) insertEventLocked(event llm.UsageEvent) {
	count := len(tracker.events)
	if count == 0 || !event.TS.Before(tracker.events[count-1].TS) {
		tracker.events = append(tracker.events, event)
		return
	}
	index := sort.Search(count, func(index int) bool {
		return tracker.events[index].TS.After(event.TS)
	})
	tracker.events = append(tracker.events, llm.UsageEvent{})
	copy(tracker.events[index+1:], tracker.events[index:])
	tracker.events[index] = event
}

func (tracker *Tracker) addDailyLocked(event llm.UsageEvent) {
	day := tracker.dayKey(event.TS)
	totals := tracker.daily[day]
	totals.calls++
	switch event.Status {
	case "success":
		totals.successes++
	case "budget_rejected":
		totals.budgetRejections++
	default:
		totals.errors++
	}
	totals.inputTokens += event.InputTokens
	totals.outputTokens += event.OutputTokens
	totals.costUSD += event.CostUSD
	tracker.daily[day] = totals
}

func (tracker *Tracker) rebuildDailyLocked() {
	tracker.daily = make(map[string]usageTotals)
	for _, event := range tracker.events {
		tracker.addDailyLocked(event)
	}
	for day := range tracker.alerted {
		if _, retained := tracker.daily[day]; !retained && day != tracker.pausedDay {
			delete(tracker.alerted, day)
		}
	}
}

func validatedEvents(events []llm.UsageEvent) []llm.UsageEvent {
	result := make([]llm.UsageEvent, 0, len(events))
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event.ID == "" || event.TS.IsZero() {
			continue
		}
		if _, found := seen[event.ID]; found {
			continue
		}
		seen[event.ID] = struct{}{}
		event.TS = event.TS.UTC()
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TS.Before(result[j].TS) })
	return result
}

func normalized(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func usageLevel(ratio float64) string {
	switch {
	case ratio >= 1:
		return "paused"
	case ratio >= 0.9:
		return "critical"
	case ratio >= 0.7:
		return "warning"
	default:
		return "normal"
	}
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
