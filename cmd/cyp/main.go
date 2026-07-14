package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/backtest"
	"github.com/wangchaozhi/cyp-agent/internal/config"
	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/venue"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		usage()
		return nil
	}
	switch arguments[0] {
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "backtest":
		return runBacktest(arguments[1:])
	case "sweep":
		return runSweep(arguments[1:])
	case "flatten":
		return runFlatten(arguments[1:])
	case "config":
		settings, err := config.Load()
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(settings.Snapshot(), "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func runBacktest(arguments []string) error {
	flags := flag.NewFlagSet("cyp backtest", flag.ContinueOnError)
	symbol := flags.String("symbol", "BTC/USDT", "market symbol")
	bars := flags.Int("bars", 300, "number of bars")
	window := flags.Int("window", 60, "moving-average window")
	seed := flags.Int64("seed", 7, "deterministic random seed")
	drift := flags.Float64("drift", 0.001, "synthetic per-bar drift")
	volatility := flags.Float64("vol", 0.01, "synthetic per-bar volatility")
	dataKind := flags.String("data", "synthetic", "data source: synthetic or cex")
	cexID := flags.String("cex", "binance", "public CEX for -data=cex: binance or okx")
	timeframe := flags.String("timeframe", "1h", "candle timeframe for -data=cex")
	jsonOutput := flags.Bool("json", false, "write the full report as JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	params := backtest.Params{
		Symbol: *symbol, Bars: *bars, Window: *window, Seed: *seed,
		Drift: *drift, Vol: *volatility, Data: *dataKind, Timeframe: *timeframe,
		FeeRate: 0.0004, SlippageBPS: 5, SpreadBPS: 2,
	}
	var report backtest.Report
	var err error
	if *dataKind == "cex" {
		var candles []contracts.Candle
		candles, err = fetchHistory(*cexID, *symbol, *timeframe, *bars, *window)
		if err != nil {
			return err
		}
		report, err = backtest.RunCandles(params, candles, backtest.DefaultStrategyConfig())
	} else {
		report, err = backtest.Run(params)
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	metrics := report.Metrics
	fmt.Printf("回测 %s · %d bars · window=%d\n", report.Symbol, report.NBars, params.Window)
	fmt.Println(strings.Repeat("-", 52))
	fmt.Printf("  期初净值   %.2f\n", metrics.InitialEquity)
	fmt.Printf("  期末净值   %.2f\n", metrics.FinalEquity)
	fmt.Printf("  总收益     %+.2f%%\n", metrics.TotalReturn*100)
	fmt.Printf("  最大回撤   %.2f%%\n", metrics.MaxDrawdown*100)
	fmt.Printf("  夏普       %.4f\n", metrics.Sharpe)
	fmt.Printf("  交易数     %d   胜率 %.1f%%\n", metrics.NTrades, metrics.WinRate*100)
	fmt.Printf("  总成本     %.2f\n", metrics.TotalCosts)
	return nil
}

func runSweep(arguments []string) error {
	flags := flag.NewFlagSet("cyp sweep", flag.ContinueOnError)
	symbol := flags.String("symbol", "BTC/USDT", "market symbol")
	bars := flags.Int("bars", 300, "number of bars")
	window := flags.Int("window", 60, "moving-average window")
	seed := flags.Int64("seed", 7, "deterministic random seed")
	drift := flags.Float64("drift", 0.001, "synthetic per-bar drift")
	volatility := flags.Float64("vol", 0.01, "synthetic per-bar volatility")
	top := flags.Int("top", 5, "number of ranked configs")
	thresholds := flags.String("thresholds", "0.08,0.12,0.18", "comma-separated entry thresholds")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	values, err := parseFloatList(*thresholds)
	if err != nil {
		return fmt.Errorf("thresholds: %w", err)
	}
	params := backtest.Params{
		Symbol: *symbol, Bars: *bars, Window: *window, Seed: *seed,
		Drift: *drift, Vol: *volatility, Data: "synthetic", Timeframe: "1h",
		FeeRate: 0.0004, SlippageBPS: 5, SpreadBPS: 2,
	}
	configs := backtest.Grid(values, []float64{1.5, 2, 3}, []float64{2, 3, 4})
	results, err := backtest.Sweep(params, configs, nil)
	if err != nil {
		return err
	}
	limit := *top
	if limit < 0 {
		limit = 0
	}
	if limit > len(results) {
		limit = len(results)
	}
	fmt.Printf("扫参 %s · %d bars · %d 组配置\n", *symbol, *bars, len(configs))
	for _, result := range results[:limit] {
		fmt.Printf("score=%+.4f return=%+.2f%% drawdown=%.2f%% trades=%d enter=%.3f kSL=%.1f kTP=%.1f\n",
			result.Score, result.Metrics.TotalReturn*100, result.Metrics.MaxDrawdown*100,
			result.Metrics.NTrades, result.Config.EnterThreshold,
			result.Config.StopVolMultiple, result.Config.TakeProfitVolMultiple)
	}
	robust, err := backtest.RobustSweep(params, configs, 0.3, 0.5, 0.5)
	if err != nil {
		return err
	}
	fmt.Printf("OOS return=%+.2f%% PBO=%.4f DSR=%.4f verdict=%s\n",
		robust.OutOfSample.TotalReturn*100, robust.PBO, robust.DeflatedSharpe, robust.Verdict)
	return nil
}

// fetchHistory pulls public OHLCV history from a read-only CEX venue. No API
// keys are involved; the venue's trading methods stay hard-disabled anyway.
func fetchHistory(cexID, symbol, timeframe string, bars, window int) ([]contracts.Candle, error) {
	source, err := venue.NewCEXVenue(venue.CEXConfig{ExchangeID: cexID})
	if err != nil {
		return nil, fmt.Errorf("build %s venue: %w", cexID, err)
	}
	defer func() { _ = source.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	candles, err := source.FetchOHLCV(ctx, symbol, timeframe, bars)
	if err != nil {
		return nil, fmt.Errorf("fetch %s %s history from %s: %w", symbol, timeframe, cexID, err)
	}
	if len(candles) <= window {
		return nil, fmt.Errorf("real history too short: %d bars (need > window=%d)", len(candles), window)
	}
	return candles, nil
}

func parseFloatList(value string) ([]float64, error) {
	parts := strings.Split(value, ",")
	result := make([]float64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parsed, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one value is required")
	}
	return result, nil
}

func usage() {
	fmt.Println("cyp-agent Go CLI")
	fmt.Println("  cyp backtest [flags]  运行回测（-data=synthetic 合成 / -data=cex 真实历史K线）")
	fmt.Println("  cyp sweep [flags]     扫参并做 OOS/PBO/DSR 验证")
	fmt.Println("  cyp flatten [flags]   应急清仓：开启 Kill Switch 并平掉运行中服务的全部持仓（-yes 确认执行）")
	fmt.Println("  cyp config            输出脱敏配置快照")
	fmt.Println("  cyp version           输出版本")
	fmt.Println("  cyp-server            启动 REST/SSE 服务")
}
