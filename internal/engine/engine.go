// Package engine 把行情、策略、风控、下单串起来：
//
//	K 线 → 策略(信号) → 风控(数量) → Broker(下单)
//
// 回测、模拟盘、实盘用的是同一个 Engine，区别只在于传进来的 K 线来源和 Broker。
package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/exchange"
	"trading-bots/internal/model"
	"trading-bots/internal/risk"
	"trading-bots/internal/strategy"
)

type Engine struct {
	symbol   string
	strategy strategy.Strategy
	broker   exchange.Broker
	risk     *risk.Manager
	log      *slog.Logger

	lastOpen  time.Time
	lastPrice decimal.Decimal
	stats     Stats
	halted    bool
}

// Stats 是运行期间的账户统计。
type Stats struct {
	Candles     int
	StartEquity decimal.Decimal
	PeakEquity  decimal.Decimal
	MaxDrawdown decimal.Decimal // 最大回撤，比例，例如 0.12 = 12%
	LastEquity  decimal.Decimal
}

func New(symbol string, s strategy.Strategy, b exchange.Broker, r *risk.Manager, log *slog.Logger) *Engine {
	return &Engine{symbol: symbol, strategy: s, broker: b, risk: r, log: log}
}

// Warmup 用历史 K 线预热策略（例如先算好均线），期间产生的信号全部忽略。
func (e *Engine) Warmup(candles []model.Candle) {
	for _, c := range candles {
		e.strategy.OnCandle(c)
		e.lastOpen = c.OpenTime
		e.lastPrice = c.Close
	}
}

// Run 持续处理 K 线，直到 channel 关闭或 ctx 取消。
func (e *Engine) Run(ctx context.Context, candles <-chan model.Candle) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c, ok := <-candles:
			if !ok {
				return nil
			}
			e.OnCandle(ctx, c)
		}
	}
}

// OnCandle 处理一根已收盘的 K 线。
func (e *Engine) OnCandle(ctx context.Context, c model.Candle) {
	// 重连时可能收到重复或过期的 K 线，直接丢掉
	if !c.OpenTime.After(e.lastOpen) {
		return
	}
	e.lastOpen = c.OpenTime
	e.lastPrice = c.Close
	now := c.CloseTime

	// 策略每根 K 线都要喂，保证指标连续；账户同步失败时只是跳过这次交易
	sig := e.strategy.OnCandle(c)
	if err := e.broker.Sync(ctx); err != nil {
		e.log.Error("同步账户失败，跳过本根 K 线", "err", err)
		return
	}
	if sig != nil {
		e.handleSignal(ctx, now, *sig, c.Close)
	}

	equity := e.equity()
	e.risk.Observe(now, equity)
	if e.risk.Halted() && !e.halted {
		e.log.Warn("触发当日亏损上限，暂停开仓", "equity", equity.StringFixed(2))
	}
	e.halted = e.risk.Halted()
	e.updateStats(equity)
}

func (e *Engine) handleSignal(ctx context.Context, now time.Time, sig model.Signal, price decimal.Decimal) {
	qty, reject := e.risk.Size(now, sig, price, e.broker.Position(e.symbol), e.broker.Cash(), e.equity())
	if reject != "" {
		e.log.Debug("信号被风控拒绝", "side", sig.Side, "reason", sig.Reason, "reject", reject)
		return
	}
	fill, err := e.broker.MarketOrder(ctx, e.symbol, sig.Side, qty, price)
	if err != nil {
		e.log.Error("下单失败", "side", sig.Side, "qty", qty, "err", err)
		return
	}
	attrs := []any{
		"time", now.UTC().Format(time.DateTime), "side", fill.Side, "reason", sig.Reason,
		"qty", fill.Qty, "price", fill.Price.StringFixed(2), "fee", fill.Fee.StringFixed(4),
	}
	if fill.Side == model.Sell && !fill.RealizedPnL.IsZero() {
		attrs = append(attrs, "pnl", fill.RealizedPnL.StringFixed(2))
	}
	e.log.Info("成交", attrs...)
	if err := e.broker.Sync(ctx); err != nil {
		e.log.Error("成交后同步账户失败", "err", err)
	}
}

func (e *Engine) equity() decimal.Decimal {
	return e.broker.Equity(map[string]decimal.Decimal{e.symbol: e.lastPrice})
}

func (e *Engine) updateStats(equity decimal.Decimal) {
	s := &e.stats
	if s.Candles == 0 {
		s.StartEquity, s.PeakEquity = equity, equity
	}
	s.Candles++
	s.LastEquity = equity
	if equity.GreaterThan(s.PeakEquity) {
		s.PeakEquity = equity
	}
	if s.PeakEquity.IsPositive() {
		if dd := s.PeakEquity.Sub(equity).Div(s.PeakEquity); dd.GreaterThan(s.MaxDrawdown) {
			s.MaxDrawdown = dd
		}
	}
}

func (e *Engine) Stats() Stats { return e.stats }
