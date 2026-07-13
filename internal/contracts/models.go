package contracts

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type Stance string
type Side string
type Instrument string
type MarginMode string
type Verdict string
type EntryType string
type OrderStatus string
type AgentID string
type ApprovalAction string
type RunStatus string

const (
	StanceBullish Stance = "bullish"
	StanceBearish Stance = "bearish"
	StanceNeutral Stance = "neutral"

	SideLong  Side = "long"
	SideShort Side = "short"
	SideFlat  Side = "flat"

	InstrumentSpot Instrument = "spot"
	InstrumentPerp Instrument = "perp"

	MarginModeIsolated MarginMode = "isolated"
	MarginModeCross    MarginMode = "cross"

	VerdictApproved  Verdict = "approved"
	VerdictDownsized Verdict = "downsized"
	VerdictRejected  Verdict = "rejected"

	EntryTypeMarket EntryType = "market"
	EntryTypeLimit  EntryType = "limit"
	EntryTypeRange  EntryType = "range"

	OrderStatusNew              OrderStatus = "new"
	OrderStatusPreflight        OrderStatus = "preflight"
	OrderStatusSubmitting       OrderStatus = "submitting"
	OrderStatusAcknowledged     OrderStatus = "acknowledged"
	OrderStatusPartiallyFilled  OrderStatus = "partially_filled"
	OrderStatusFilled           OrderStatus = "filled"
	OrderStatusProtectivePlaced OrderStatus = "protective_placed"
	OrderStatusClosed           OrderStatus = "closed"
	OrderStatusCanceled         OrderStatus = "canceled"
	OrderStatusRejected         OrderStatus = "rejected"
	OrderStatusFailed           OrderStatus = "failed"
	OrderStatusUnknown          OrderStatus = "unknown"
	OrderStatusProtectiveFailed OrderStatus = "protective_failed"
	OrderStatusFlattening       OrderStatus = "flattening"

	AgentTechnical   AgentID = "technical"
	AgentDerivatives AgentID = "derivatives"
	AgentSentiment   AgentID = "sentiment"
	AgentOnchain     AgentID = "onchain"

	ApprovalApprove ApprovalAction = "approve"
	ApprovalReject  ApprovalAction = "reject"
	ApprovalModify  ApprovalAction = "modify"

	RunNoTrade         RunStatus = "no_trade"
	RunRejected        RunStatus = "rejected"
	RunNotApproved     RunStatus = "not_approved"
	RunExecuted        RunStatus = "executed"
	RunExecutionFailed RunStatus = "execution_failed"
	RunError           RunStatus = "error"
)

// PriceLevel is encoded as the existing [price, size] JSON tuple.
type PriceLevel [2]Decimal

type Candle struct {
	TS     time.Time `json:"ts"`
	Open   Decimal   `json:"open"`
	High   Decimal   `json:"high"`
	Low    Decimal   `json:"low"`
	Close  Decimal   `json:"close"`
	Volume Decimal   `json:"volume"`
}

type OrderBook struct {
	Bids List[PriceLevel] `json:"bids"`
	Asks List[PriceLevel] `json:"asks"`
}

type DerivativesData struct {
	FundingRate     *Decimal `json:"funding_rate"`
	OpenInterest    *Decimal `json:"open_interest"`
	LongShortRatio  *Decimal `json:"long_short_ratio"`
	Basis           *Decimal `json:"basis"`
	Liquidations24H *Decimal `json:"liquidations_24h"`
}

type OnchainData struct {
	SmartMoneyFlow      *Decimal `json:"smart_money_flow"`
	LiquidityUSD        *Decimal `json:"liquidity_usd"`
	HolderConcentration *Decimal `json:"holder_concentration"`
	ExchangeNetflow     *Decimal `json:"exchange_netflow"`
}

type SentimentData struct {
	FearGreed  *int     `json:"fear_greed"`
	NewsScore  *Decimal `json:"news_score"`
	SocialHeat *Decimal `json:"social_heat"`
}

type MarketSnapshot struct {
	Symbol      string           `json:"symbol"`
	Venue       string           `json:"venue"`
	TS          time.Time        `json:"ts"`
	OHLCV       List[Candle]     `json:"ohlcv"`
	OrderBook   *OrderBook       `json:"orderbook"`
	Derivatives *DerivativesData `json:"derivatives"`
	Onchain     *OnchainData     `json:"onchain"`
	Sentiment   *SentimentData   `json:"sentiment"`
}

type Signal struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

type AnalystReport struct {
	Agent      AgentID      `json:"agent"`
	Stance     Stance       `json:"stance"`
	Confidence float64      `json:"confidence"`
	Signals    List[Signal] `json:"signals"`
	Rationale  string       `json:"rationale"`
	Degraded   bool         `json:"degraded"`
}

type PricePlan struct {
	Type  EntryType `json:"type"`
	Price *Decimal  `json:"price"`
	Low   *Decimal  `json:"low"`
	High  *Decimal  `json:"high"`
}

type TradeProposal struct {
	Symbol            string        `json:"symbol"`
	Venue             string        `json:"venue"`
	Side              Side          `json:"side"`
	Instrument        Instrument    `json:"instrument"`
	SizeQuote         Decimal       `json:"size_quote"`
	Leverage          float64       `json:"leverage"`
	MarginMode        MarginMode    `json:"margin_mode"`
	Entry             PricePlan     `json:"entry"`
	StopLoss          *Decimal      `json:"stop_loss"`
	TakeProfit        List[Decimal] `json:"take_profit"`
	Confidence        float64       `json:"confidence"`
	Thesis            string        `json:"thesis"`
	SupportingReports List[string]  `json:"supporting_reports"`
}

type RiskAssessment struct {
	Verdict           Verdict      `json:"verdict"`
	HardViolations    List[string] `json:"hard_violations"`
	AdjustedSizeQuote *Decimal     `json:"adjusted_size_quote"`
	LLMNotes          string       `json:"llm_notes"`
	RiskScore         float64      `json:"risk_score"`
	LLMReviewed       bool         `json:"llm_reviewed"`
}

type ApprovalDecision struct {
	Decision ApprovalAction `json:"decision"`
	Modified *TradeProposal `json:"modified"`
	Operator string         `json:"operator"`
	TS       time.Time      `json:"ts"`
	Note     string         `json:"note"`
}

type OrderIntent struct {
	ClientID       string        `json:"client_id"`
	Symbol         string        `json:"symbol"`
	Venue          string        `json:"venue"`
	Side           Side          `json:"side"`
	Instrument     Instrument    `json:"instrument"`
	OrderType      EntryType     `json:"order_type"`
	SizeQuote      Decimal       `json:"size_quote"`
	Price          *Decimal      `json:"price"`
	Leverage       float64       `json:"leverage"`
	MarginMode     MarginMode    `json:"margin_mode"`
	ReduceOnly     bool          `json:"reduce_only"`
	StopLoss       *Decimal      `json:"stop_loss"`
	TakeProfit     List[Decimal] `json:"take_profit"`
	Chain          *string       `json:"chain"`
	ApprovalAmount *Decimal      `json:"approval_amount"`
}

type ProtectiveOrder struct {
	Kind         string  `json:"kind"`
	OrderID      string  `json:"order_id"`
	TriggerPrice Decimal `json:"trigger_price"`
	ReduceOnly   bool    `json:"reduce_only"`
}

type ExecutionResult struct {
	ClientID         string                `json:"client_id"`
	OrderID          *string               `json:"order_id"`
	Status           OrderStatus           `json:"status"`
	FilledBase       Decimal               `json:"filled_base"`
	AvgPrice         *Decimal              `json:"avg_price"`
	FeeQuote         Decimal               `json:"fee_quote"`
	SlippageBPS      *Decimal              `json:"slippage_bps"`
	ProtectiveOrders List[ProtectiveOrder] `json:"protective_orders"`
	Error            *string               `json:"error"`
	Chain            *string               `json:"chain"`
	TxHash           *string               `json:"tx_hash"`
	GasUsed          *Decimal              `json:"gas_used"`
}

type Position struct {
	Symbol     string      `json:"symbol"`
	Venue      string      `json:"venue"`
	Side       Side        `json:"side"`
	Instrument Instrument  `json:"instrument"`
	SizeBase   Decimal     `json:"size_base"`
	EntryPrice Decimal     `json:"entry_price"`
	Leverage   float64     `json:"leverage"`
	LiqPrice   *Decimal    `json:"liq_price"`
	MarginMode *MarginMode `json:"margin_mode"`
	Chain      *string     `json:"chain"`
	TxHash     *string     `json:"tx_hash"`
}

func (p Position) NotionalAt(price Decimal) Decimal { return p.SizeBase.Mul(price) }

func (p Position) MarginUsed() (*Decimal, error) {
	if p.Instrument != InstrumentPerp {
		return nil, nil
	}
	leverage, err := ParseDecimal(fmt.Sprintf("%.17g", p.Leverage))
	if err != nil || !leverage.IsPositive() {
		return nil, errors.New("leverage must be positive")
	}
	margin, err := p.SizeBase.Mul(p.EntryPrice).Quo(leverage)
	if err != nil {
		return nil, err
	}
	return &margin, nil
}

type Balances struct {
	QuoteCCY   string  `json:"quote_ccy"`
	FreeQuote  Decimal `json:"free_quote"`
	TotalQuote Decimal `json:"total_quote"`
}

type TradeReview struct {
	Symbol      string       `json:"symbol"`
	ProposalRef string       `json:"proposal_ref"`
	Kind        string       `json:"kind"`
	Score       float64      `json:"score"`
	PNLQuote    Decimal      `json:"pnl_quote"`
	SlippageBPS *Decimal     `json:"slippage_bps"`
	Lessons     List[string] `json:"lessons"`
	Notes       string       `json:"notes"`
	TS          time.Time    `json:"ts"`
}

type RunResult struct {
	RunID      string              `json:"run_id"`
	Symbol     string              `json:"symbol"`
	Status     RunStatus           `json:"status"`
	Reports    List[AnalystReport] `json:"reports"`
	Proposal   *TradeProposal      `json:"proposal"`
	Assessment *RiskAssessment     `json:"assessment"`
	Decision   *ApprovalDecision   `json:"decision"`
	Execution  *ExecutionResult    `json:"execution"`
	Review     *TradeReview        `json:"review"`
	Error      *string             `json:"error"`
}

// RiskContext is the deterministic input assembled by the orchestrator. All
// optional preflight fields stay nil when the upstream source is unavailable.
type RiskContext struct {
	EquityQuote               Decimal  `json:"equity_quote"`
	RefPrice                  Decimal  `json:"ref_price"`
	GrossExposureQuote        Decimal  `json:"gross_exposure_quote"`
	SymbolExposureQuote       Decimal  `json:"symbol_exposure_quote"`
	CorrelatedExposureQuote   *Decimal `json:"correlated_exposure_quote"`
	PortfolioVARQuote         *Decimal `json:"portfolio_var_quote"`
	PortfolioCVARQuote        *Decimal `json:"portfolio_cvar_quote"`
	OrdersLastHour            int      `json:"orders_last_hour"`
	ConsecutiveLosses         int      `json:"consecutive_losses"`
	DailyDrawdown             Decimal  `json:"daily_drawdown"`
	WeeklyDrawdown            Decimal  `json:"weekly_drawdown"`
	TotalDrawdown             Decimal  `json:"total_drawdown"`
	Reconciling               bool     `json:"reconciling"`
	Kill                      bool     `json:"kill"`
	MarginRatio               *Decimal `json:"margin_ratio"`
	EstimatedSlippageBPS      *Decimal `json:"est_slippage_bps"`
	EstimatedLiquidationPrice *Decimal `json:"est_liq_price"`
	EstimatedPriceImpact      *Decimal `json:"est_price_impact"`
	Onchain                   bool     `json:"onchain"`
	ApprovalAmount            *Decimal `json:"approval_amount"`
	ApprovalUnlimited         bool     `json:"approval_unlimited"`
	ContractAddress           *string  `json:"contract_address"`
	PoolTVLUSD                *Decimal `json:"pool_tvl_usd"`
	EstimatedGasQuote         *Decimal `json:"est_gas_quote"`
	MEVProtected              *bool    `json:"mev_protected"`
}

func (p TradeProposal) Validate() error {
	if strings.TrimSpace(p.Symbol) == "" || strings.TrimSpace(p.Venue) == "" {
		return errors.New("symbol and venue are required")
	}
	if p.Side != SideLong && p.Side != SideShort && p.Side != SideFlat {
		return fmt.Errorf("invalid side %q", p.Side)
	}
	if p.Instrument != InstrumentSpot && p.Instrument != InstrumentPerp {
		return fmt.Errorf("invalid instrument %q", p.Instrument)
	}
	if p.SizeQuote.IsNegative() {
		return errors.New("size_quote cannot be negative")
	}
	if !finiteInRange(p.Confidence, 0, 1) {
		return errors.New("confidence must be finite and between 0 and 1")
	}
	if math.IsNaN(p.Leverage) || math.IsInf(p.Leverage, 0) || p.Leverage < 1 {
		return errors.New("leverage must be finite and at least 1")
	}
	return nil
}

func (a RiskAssessment) Validate() error {
	if a.Verdict != VerdictApproved && a.Verdict != VerdictDownsized && a.Verdict != VerdictRejected {
		return fmt.Errorf("invalid verdict %q", a.Verdict)
	}
	if !finiteInRange(a.RiskScore, 0, 1) {
		return errors.New("risk_score must be finite and between 0 and 1")
	}
	if a.AdjustedSizeQuote != nil && a.AdjustedSizeQuote.IsNegative() {
		return errors.New("adjusted_size_quote cannot be negative")
	}
	return nil
}

func finiteInRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}
