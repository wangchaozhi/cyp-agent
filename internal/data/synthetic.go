package data

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/contracts"
)

type Source interface {
	Snapshot(context.Context, string) (contracts.MarketSnapshot, error)
}

type SyntheticOption func(*SyntheticMarketData)

func WithSyntheticBase(value contracts.Decimal) SyntheticOption {
	return func(source *SyntheticMarketData) { source.base = value }
}

func WithSyntheticBars(value int) SyntheticOption {
	return func(source *SyntheticMarketData) { source.bars = value }
}

func WithSyntheticSeed(value int64) SyntheticOption {
	return func(source *SyntheticMarketData) { source.seed = value }
}

func WithSyntheticVolatility(value float64) SyntheticOption {
	return func(source *SyntheticMarketData) { source.volatility = value }
}

func WithSyntheticDrift(value float64) SyntheticOption {
	return func(source *SyntheticMarketData) { source.drift = value }
}

func WithLiveTicks(value bool) SyntheticOption {
	return func(source *SyntheticMarketData) { source.liveTicks = value }
}

func WithSyntheticClock(clock func() time.Time) SyntheticOption {
	return func(source *SyntheticMarketData) {
		if clock != nil {
			source.now = clock
		}
	}
}

// SyntheticMarketData is a deterministic, network-free random walk. Live tick
// state is protected independently so concurrent symbols remain safe.
type SyntheticMarketData struct {
	base       contracts.Decimal
	bars       int
	seed       int64
	volatility float64
	drift      float64
	liveTicks  bool
	now        func() time.Time

	mu    sync.Mutex
	ticks map[string]uint64
	marks map[string]float64
}

func NewSyntheticMarketData(options ...SyntheticOption) *SyntheticMarketData {
	source := &SyntheticMarketData{
		base:       contracts.MustDecimal("60000"),
		bars:       200,
		seed:       7,
		volatility: 0.01,
		drift:      0.0005,
		now:        time.Now,
		ticks:      make(map[string]uint64),
		marks:      make(map[string]float64),
	}
	for _, option := range options {
		option(source)
	}
	if !source.base.IsPositive() || source.bars < 0 || source.volatility < 0 ||
		math.IsNaN(source.volatility) || math.IsInf(source.volatility, 0) ||
		math.IsNaN(source.drift) || math.IsInf(source.drift, 0) {
		panic("invalid synthetic market configuration")
	}
	return source
}

func stableSeed(parts ...string) int64 {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%v", len(parts), parts)))
	return int64(binary.LittleEndian.Uint64(hash[:8]))
}

func decimalFixed(value float64, precision int) (contracts.Decimal, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return contracts.Zero(), errors.New("non-finite synthetic value")
	}
	return contracts.ParseDecimal(strconv.FormatFloat(value, 'f', precision, 64))
}

func (s *SyntheticMarketData) Snapshot(
	ctx context.Context,
	symbol string,
) (contracts.MarketSnapshot, error) {
	if err := ctxErr(ctx); err != nil {
		return contracts.MarketSnapshot{}, err
	}
	base, err := s.base.Float64()
	if err != nil {
		return contracts.MarketSnapshot{}, err
	}
	rng := rand.New(rand.NewSource(stableSeed(strconv.FormatInt(s.seed, 10), symbol)))
	price := base
	now := s.now().UTC()
	candles := make(contracts.List[contracts.Candle], 0, s.bars)
	for index := 0; index < s.bars; index++ {
		if err := ctxErr(ctx); err != nil {
			return contracts.MarketSnapshot{}, err
		}
		periodReturn := s.drift + rng.NormFloat64()*s.volatility
		openPrice := price
		closePrice := math.Max(0.01, price*(1+periodReturn))
		highPrice := math.Max(openPrice, closePrice) * (1 + math.Abs(rng.NormFloat64()*s.volatility/2))
		lowPrice := math.Min(openPrice, closePrice) * (1 - math.Abs(rng.NormFloat64()*s.volatility/2))
		volume := math.Abs(1000 + rng.NormFloat64()*200)
		openDecimal, parseErr := decimalFixed(openPrice, 2)
		if parseErr != nil {
			return contracts.MarketSnapshot{}, parseErr
		}
		highDecimal, _ := decimalFixed(highPrice, 2)
		lowDecimal, _ := decimalFixed(lowPrice, 2)
		closeDecimal, _ := decimalFixed(closePrice, 2)
		volumeDecimal, _ := decimalFixed(volume, 4)
		candles = append(candles, contracts.Candle{
			TS:     now.Add(-time.Duration(s.bars-index) * time.Hour),
			Open:   openDecimal,
			High:   highDecimal,
			Low:    lowDecimal,
			Close:  closeDecimal,
			Volume: volumeDecimal,
		})
		price = closePrice
	}

	if s.liveTicks && len(candles) > 0 {
		s.mu.Lock()
		tick := s.ticks[symbol] + 1
		s.ticks[symbol] = tick
		tickRNG := rand.New(rand.NewSource(stableSeed(
			strconv.FormatInt(s.seed, 10), symbol, "tick", strconv.FormatUint(tick, 10),
		)))
		mark, exists := s.marks[symbol]
		if !exists {
			mark, _ = candles[len(candles)-1].Close.Float64()
		}
		mark = math.Max(0.01, mark*(1+s.drift/24+tickRNG.NormFloat64()*s.volatility/16))
		s.marks[symbol] = mark
		s.mu.Unlock()
		previous := candles[len(candles)-1].Open
		if len(candles) > 1 {
			previous = candles[len(candles)-2].Close
		}
		markDecimal, _ := decimalFixed(mark, 2)
		latest := candles[len(candles)-1]
		latest.Open = previous
		latest.Close = markDecimal
		latest.High = previous
		latest.Low = previous
		if markDecimal.Cmp(previous) > 0 {
			latest.High = markDecimal
		} else {
			latest.Low = markDecimal
		}
		candles[len(candles)-1] = latest
	}

	funding, _ := decimalFixed(rng.Float64()*0.001-0.0005, 6)
	openInterest, _ := decimalFixed(100_000_000+rng.Float64()*400_000_000, 0)
	longShort, _ := decimalFixed(0.8+rng.Float64()*0.5, 3)
	fearGreed := 20 + rng.Intn(61)
	smartMoney, _ := decimalFixed(rng.NormFloat64()*500_000, 0)
	netflow, _ := decimalFixed(rng.NormFloat64()*200_000, 0)
	liquidity, _ := decimalFixed(5_000_000+rng.Float64()*45_000_000, 0)
	return contracts.MarketSnapshot{
		Symbol: symbol,
		Venue:  "synthetic",
		TS:     now,
		OHLCV:  candles,
		Derivatives: &contracts.DerivativesData{
			FundingRate:    &funding,
			OpenInterest:   &openInterest,
			LongShortRatio: &longShort,
		},
		Sentiment: &contracts.SentimentData{FearGreed: &fearGreed},
		Onchain: &contracts.OnchainData{
			SmartMoneyFlow:  &smartMoney,
			ExchangeNetflow: &netflow,
			LiquidityUSD:    &liquidity,
		},
	}, nil
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
