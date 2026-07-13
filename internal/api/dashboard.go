package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
	"github.com/wangchaozhi/cyp-agent/internal/portfolio"
	"github.com/wangchaozhi/cyp-agent/internal/tokenusage"
)

type positionView struct {
	Symbol           string                `json:"symbol"`
	Venue            string                `json:"venue"`
	Side             contracts.Side        `json:"side"`
	Instrument       contracts.Instrument  `json:"instrument"`
	SizeBase         contracts.Decimal     `json:"size_base"`
	EntryPrice       contracts.Decimal     `json:"entry_price"`
	Leverage         float64               `json:"leverage"`
	LiquidationPrice *contracts.Decimal    `json:"liq_price"`
	MarginMode       *contracts.MarginMode `json:"margin_mode"`
	Chain            *string               `json:"chain"`
	TxHash           *string               `json:"tx_hash"`
	MarkPrice        contracts.Decimal     `json:"mark_price"`
	Notional         contracts.Decimal     `json:"notional"`
	UnrealizedPNL    contracts.Decimal     `json:"unrealized_pnl"`
	UnrealizedPNLPct contracts.Decimal     `json:"unrealized_pnl_pct"`
	MarginUsed       *contracts.Decimal    `json:"margin_used"`
	FundingRate      *contracts.Decimal    `json:"funding_rate"`
}

func (s *Server) positions(w http.ResponseWriter, request *http.Request) {
	snapshot, err := s.accountCache.Load(request.Context(), s.venue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]positionView, 0, len(snapshot.positions))
	for _, position := range snapshot.positions {
		views = append(views, positionViewAtMark(position, snapshot.marks[position.Symbol]))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) trades(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.riskState.Trades())
}

func positionViewAtMark(position contracts.Position, mark contracts.Decimal) positionView {
	if !mark.IsPositive() {
		mark = position.EntryPrice
	}
	direction := contracts.NewDecimalFromInt64(1)
	if position.Side == contracts.SideShort {
		direction = direction.Neg()
	}
	entryNotional := position.SizeBase.Mul(position.EntryPrice)
	unrealized := position.SizeBase.Mul(mark.Sub(position.EntryPrice)).Mul(direction)
	unrealizedPct := contracts.Zero()
	if entryNotional.IsPositive() {
		if value, err := unrealized.Quo(entryNotional); err == nil {
			unrealizedPct = value
		}
	}
	margin, _ := position.MarginUsed()
	return positionView{
		Symbol: position.Symbol, Venue: position.Venue, Side: position.Side,
		Instrument: position.Instrument, SizeBase: position.SizeBase,
		EntryPrice: position.EntryPrice, Leverage: position.Leverage,
		LiquidationPrice: position.LiqPrice, MarginMode: position.MarginMode,
		Chain: position.Chain, TxHash: position.TxHash,
		MarkPrice: mark, Notional: position.SizeBase.Mul(mark),
		UnrealizedPNL: unrealized, UnrealizedPNLPct: unrealizedPct,
		MarginUsed: margin, FundingRate: nil,
	}
}

func (s *Server) closePosition(w http.ResponseWriter, request *http.Request) {
	var payload contracts.ClosePositionRequest
	if err := decodeJSON(request, &payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	payload.Symbol = strings.TrimSpace(payload.Symbol)
	if payload.Instrument == "" {
		payload.Instrument = contracts.InstrumentSpot
	}
	if s.symbolLocks != nil {
		if err := s.symbolLocks.Do(request.Context(), payload.Symbol, func(context.Context) error {
			s.closePositionLocked(w, request, payload)
			return nil
		}); err != nil {
			writeError(w, http.StatusConflict, "该币种正在执行其他操作，请稍后重试")
		}
		return
	}
	s.closePositionLocked(w, request, payload)
}

func (s *Server) closePositionLocked(
	w http.ResponseWriter,
	request *http.Request,
	payload contracts.ClosePositionRequest,
) {
	positions, err := s.venue.Positions(request.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var found *contracts.Position
	for i := range positions {
		if positions[i].Symbol == payload.Symbol && positions[i].Instrument == payload.Instrument {
			position := positions[i]
			found = &position
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "无此持仓")
		return
	}
	mark, err := s.venue.FetchTicker(request.Context(), found.Symbol)
	if err != nil || !mark.IsPositive() {
		mark = found.EntryPrice
	}
	marginMode := contracts.MarginModeIsolated
	if found.MarginMode != nil {
		marginMode = *found.MarginMode
	}
	clientID := "manual-close-" + shortID()
	eventPrefix := "manual-close:" + clientID
	result, executeErr := s.orchestrator.ExecuteReduceOnly(request.Context(), eventPrefix, contracts.OrderIntent{
		ClientID: clientID, Symbol: found.Symbol, Venue: s.venue.ID(),
		Side: found.Side, Instrument: found.Instrument, OrderType: contracts.EntryTypeMarket,
		SizeQuote: found.SizeBase.Mul(mark), Price: &mark, Leverage: found.Leverage,
		MarginMode: marginMode, ReduceOnly: true,
		TakeProfit: contracts.List[contracts.Decimal]{},
	})
	if executeErr != nil && result.Status != contracts.OrderStatusFilled {
		writeError(w, http.StatusBadRequest, executeErr.Error())
		return
	}
	if result.Status != contracts.OrderStatusFilled {
		detail := "平仓失败"
		if result.Error != nil && *result.Error != "" {
			detail = *result.Error
		}
		writeError(w, http.StatusBadRequest, detail)
		return
	}
	s.accountCache.Invalidate()
	finalizeErr := s.orchestrator.FinalizeClose(request.Context(), *found)
	positionFlat := finalizeErr == nil
	if finalizeErr != nil {
		s.logger.ErrorContext(request.Context(), "manual_close_finalize_failed", "error", finalizeErr.Error())
		if s.safety != nil {
			s.safety.Freeze("manual close protection cleanup failed")
		}
		executeErr = errors.Join(executeErr, finalizeErr)
		verifiedFlat, verifyErr := s.orchestrator.PositionFlat(request.Context(), *found)
		positionFlat = verifyErr == nil && verifiedFlat
		if verifyErr != nil {
			executeErr = errors.Join(executeErr, verifyErr)
		} else if !verifiedFlat {
			executeErr = errors.Join(executeErr, errors.New("平仓后仍存在剩余持仓"))
		}
	} else if completeErr := s.orchestrator.CompleteReduceOnly(request.Context(), eventPrefix, clientID, result); completeErr != nil {
		executeErr = errors.Join(executeErr, completeErr)
	}
	if s.riskState != nil && positionFlat {
		balances, balanceErr := s.venue.Balances(request.Context())
		if balanceErr != nil {
			s.logger.ErrorContext(request.Context(), "risk_state_close_balance_failed", "error", balanceErr.Error())
			if s.safety != nil {
				s.safety.Freeze("manual close balance reconciliation failed")
			}
		} else {
			equity := balances.TotalQuote
			if !equity.IsPositive() {
				equity = balances.FreeQuote
			}
			reference := result.ClientID
			if opened, ok := s.riskState.OpenTrade(found.Symbol, found.Instrument); ok && opened.RunID != "" {
				reference = opened.RunID
			}
			record, stateErr := s.riskState.RecordClose(request.Context(), reference, *found, result, equity)
			if stateErr != nil {
				s.logger.ErrorContext(request.Context(), "risk_state_close_persist_failed", "error", stateErr.Error())
				if s.safety != nil {
					s.safety.Freeze("manual close risk state persistence failed")
				}
			} else if _, reviewErr := s.orchestrator.ReviewClosed(
				request.Context(), *found, result, record.PNLQuote, reference,
			); reviewErr != nil {
				s.logger.ErrorContext(request.Context(), "close_review_failed", "error", reviewErr.Error())
			}
		}
	}
	if executeErr != nil {
		writeError(w, http.StatusServiceUnavailable, "平仓已成交，但订单审计或保护单清理失败；系统已冻结，请重新对账")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) metricsSnapshot(w http.ResponseWriter, _ *http.Request) {
	response := map[string]any{
		"runs": s.metrics.Snapshot(), "llm": s.orchestrator.LLMMetrics(),
		"runtime": s.runtimeMetrics.Snapshot(),
	}
	if s.tokenUsage != nil {
		response["token_usage"] = s.tokenUsage.Snapshot()
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) tokenUsageReport(w http.ResponseWriter, request *http.Request) {
	if s.tokenUsage == nil {
		writeJSON(w, http.StatusOK, tokenusage.EmptyReport())
		return
	}
	days, _ := strconv.Atoi(request.URL.Query().Get("days"))
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	bucket := strings.TrimSpace(request.URL.Query().Get("bucket"))
	writeJSON(w, http.StatusOK, s.tokenUsage.Report(days, bucket, limit))
}

type drawdownSnapshot struct {
	Daily  contracts.Decimal `json:"daily"`
	Weekly contracts.Decimal `json:"weekly"`
	Total  contracts.Decimal `json:"total"`
}

type riskLimitSnapshot struct {
	DailyDrawdown        contracts.Decimal `json:"daily_dd"`
	WeeklyDrawdown       contracts.Decimal `json:"weekly_dd"`
	TotalDrawdown        contracts.Decimal `json:"total_dd"`
	MaxLeverage          contracts.Decimal `json:"max_leverage"`
	MaxMarginPct         contracts.Decimal `json:"max_margin_pct"`
	LeverageStep         contracts.Decimal `json:"leverage_step"`
	MinLiqBuffer         contracts.Decimal `json:"min_liq_buffer"`
	MaxOrdersPerHour     int               `json:"max_orders_per_hour"`
	MaxConsecutiveLosses int               `json:"max_consecutive_losses"`
	MinMarginRatio       contracts.Decimal `json:"min_margin_ratio"`
}

type riskBoard struct {
	Mode              string             `json:"mode"`
	Kill              bool               `json:"kill"`
	Equity            contracts.Decimal  `json:"equity"`
	Drawdown          drawdownSnapshot   `json:"drawdown"`
	OrdersLastHour    int                `json:"orders_last_hour"`
	ConsecutiveLosses int                `json:"consecutive_losses"`
	RealizedPNL       contracts.Decimal  `json:"realized_pnl"`
	PortfolioCVAR     *contracts.Decimal `json:"portfolio_cvar_quote"`
	CVaRSamples       int                `json:"cvar_samples"`
	MarginRatio       *contracts.Decimal `json:"margin_ratio"`
	PerpetualNotional contracts.Decimal  `json:"perp_notional"`
	Limits            riskLimitSnapshot  `json:"limits"`
	LiveGuard         any                `json:"live_guard"`
}

func (s *Server) riskSnapshot(w http.ResponseWriter, request *http.Request) {
	settings := s.control.Settings()
	snapshot, err := s.accountCache.Load(request.Context(), s.venue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	equity := snapshot.balances.TotalQuote
	if !equity.IsPositive() {
		equity = snapshot.balances.FreeQuote
	}
	perpetualNotional := contracts.Zero()
	for _, position := range snapshot.positions {
		if position.Instrument == contracts.InstrumentPerp {
			perpetualNotional = perpetualNotional.Add(position.NotionalAt(position.EntryPrice))
		}
	}
	var marginRatio *contracts.Decimal
	if perpetualNotional.IsPositive() {
		if ratio, err := equity.Quo(perpetualNotional); err == nil {
			marginRatio = &ratio
		}
	}
	riskConfig := settings.Risk
	riskState := s.riskState.Snapshot(equity)
	writeJSON(w, http.StatusOK, riskBoard{
		Mode: settings.Mode, Kill: settings.Kill, Equity: equity,
		Drawdown: drawdownSnapshot{
			Daily: riskState.DailyDrawdown, Weekly: riskState.WeeklyDrawdown, Total: riskState.TotalDrawdown,
		},
		OrdersLastHour: riskState.OrdersLastHour, ConsecutiveLosses: riskState.ConsecutiveLosses,
		RealizedPNL: riskState.RealizedPNL, PortfolioCVAR: riskState.PortfolioCVARQuote,
		CVaRSamples: riskState.CVaRSamples, MarginRatio: marginRatio,
		PerpetualNotional: perpetualNotional,
		Limits: riskLimitSnapshot{
			DailyDrawdown:        riskConfig.DailyDrawdownLimit,
			WeeklyDrawdown:       riskConfig.WeeklyDrawdownLimit,
			TotalDrawdown:        riskConfig.MaxDrawdownLimit,
			MaxLeverage:          riskConfig.MaxLeverage,
			MaxMarginPct:         riskConfig.MaxMarginPct,
			LeverageStep:         riskConfig.LeverageStep,
			MinLiqBuffer:         riskConfig.MinLiqBuffer,
			MaxOrdersPerHour:     riskConfig.MaxOrdersPerHour,
			MaxConsecutiveLosses: riskConfig.MaxConsecutiveLosses,
			MinMarginRatio:       riskConfig.MinMarginRatio,
		},
		LiveGuard: settings.LiveGuard(),
	})
}

func (s *Server) portfolioSnapshot(w http.ResponseWriter, request *http.Request) {
	settings := s.control.Settings()
	snapshot, err := s.accountCache.Load(request.Context(), s.venue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	equity := snapshot.balances.TotalQuote
	if !equity.IsPositive() {
		equity = snapshot.balances.FreeQuote
	}
	marks := s.orchestrator.Marks()
	for symbol, mark := range snapshot.marks {
		marks[symbol] = mark
	}
	writeJSON(w, http.StatusOK, portfolio.Build(
		snapshot.positions, marks, equity, settings.Risk.MaxCorrelatedExposure,
	))
}
