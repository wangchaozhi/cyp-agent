package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

// flattenPosition mirrors the /api/positions response fields the emergency
// tool needs; extra fields are ignored on purpose.
type flattenPosition struct {
	Symbol     string               `json:"symbol"`
	Venue      string               `json:"venue"`
	Side       contracts.Side       `json:"side"`
	Instrument contracts.Instrument `json:"instrument"`
	SizeBase   contracts.Decimal    `json:"size_base"`
	MarkPrice  contracts.Decimal    `json:"mark_price"`
	Notional   contracts.Decimal    `json:"notional"`
}

type flattenClient struct {
	base   string
	token  string
	client *http.Client
}

// runFlatten is the emergency liquidation tool: it talks to a running
// cyp-server over REST, engages the kill switch, then closes every position
// on the current execution venue one by one through the same durable
// reduce-only path the dashboard uses (which also sweeps protective orders).
func runFlatten(arguments []string) error {
	flags := flag.NewFlagSet("cyp flatten", flag.ContinueOnError)
	base := flags.String("base", "http://127.0.0.1:8080", "cyp-server base URL")
	token := flags.String("token", os.Getenv("CYP_API_TOKEN"), "API token (defaults to CYP_API_TOKEN)")
	yes := flags.Bool("yes", false, "actually flatten; without it the tool only prints what would be closed")
	keepKill := flags.Bool("no-kill", false, "do not engage the kill switch first (not recommended)")
	timeout := flags.Duration("timeout", 60*time.Second, "per-request timeout")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	api := &flattenClient{
		base:   strings.TrimRight(strings.TrimSpace(*base), "/"),
		token:  strings.TrimSpace(*token),
		client: &http.Client{Timeout: *timeout},
	}
	ctx := context.Background()

	positions := make([]flattenPosition, 0)
	if err := api.call(ctx, http.MethodGet, "/api/positions", nil, &positions); err != nil {
		return fmt.Errorf("加载持仓失败: %w", err)
	}
	if len(positions) == 0 {
		fmt.Println("当前执行场所无持仓，无需清仓。")
		return nil
	}
	fmt.Printf("发现 %d 个持仓：\n", len(positions))
	for _, position := range positions {
		fmt.Printf("  %-18s %-5s %-4s size=%s notional=%s\n",
			position.Symbol, position.Instrument, position.Side,
			position.SizeBase, position.Notional)
	}
	if !*yes {
		fmt.Println("\n预览模式：未执行任何平仓。确认后加 -yes 重新运行。")
		return nil
	}

	if !*keepKill {
		var killStatus contracts.KillStatus
		if err := api.call(ctx, http.MethodPost, "/api/killswitch",
			contracts.KillRequest{On: true}, &killStatus); err != nil {
			return fmt.Errorf("开启 Kill Switch 失败（可用 -no-kill 跳过）: %w", err)
		}
		fmt.Println("Kill Switch 已开启，新开仓已阻断。")
	}

	var failures []string
	for _, position := range positions {
		fmt.Printf("平仓 %s (%s) ... ", position.Symbol, position.Instrument)
		err := api.call(ctx, http.MethodPost, "/api/positions/close", contracts.ClosePositionRequest{
			Symbol: position.Symbol, Instrument: position.Instrument,
		}, nil)
		if err != nil {
			fmt.Printf("失败: %v\n", err)
			failures = append(failures, fmt.Sprintf("%s: %v", position.Symbol, err))
			continue
		}
		fmt.Println("完成")
	}

	remaining := make([]flattenPosition, 0)
	if err := api.call(ctx, http.MethodGet, "/api/positions", nil, &remaining); err != nil {
		failures = append(failures, fmt.Sprintf("复核持仓失败: %v", err))
	} else if len(remaining) != 0 {
		for _, position := range remaining {
			failures = append(failures, fmt.Sprintf("%s 仍有持仓 size=%s", position.Symbol, position.SizeBase))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("应急清仓未完全成功，请立即人工处理：\n  %s", strings.Join(failures, "\n  "))
	}
	fmt.Println("应急清仓完成：所有持仓已平，保护单已清理。")
	return nil
}

func (api *flattenClient) call(ctx context.Context, method, path string, payload, target any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, api.base+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if api.token != "" {
		request.Header.Set("X-CYP-API-Token", api.token)
	}
	response, err := api.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &apiError) == nil && strings.TrimSpace(apiError.Detail) != "" {
			return fmt.Errorf("HTTP %d: %s", response.StatusCode, apiError.Detail)
		}
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	return nil
}
