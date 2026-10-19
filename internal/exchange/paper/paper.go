// Package paper 是模拟盘 Broker：不连交易所，只在内存里记账。
// 回测和实时模拟盘都用它。
package paper

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

var ErrInsufficientFunds = errors.New("资金不足")

type position struct {
	qty decimal.Decimal
	// costBasis 是当前持仓的总成本（含买入手续费），用于计算已实现盈亏。
	costBasis decimal.Decimal
}

type Broker struct {
	mu        sync.Mutex
	cash      decimal.Decimal
	positions map[string]*position
	feeRate   decimal.Decimal // 手续费率，例如 0.001 = 0.1%
	slippage  decimal.Decimal // 滑点比例，例如 0.0005 = 5 个基点
	fills     []model.Fill
	// Now 用来给成交打时间戳。回测时由引擎设置成 K 线时间。
	Now func() time.Time
}

func New(cash, feeRate, slippage decimal.Decimal) *Broker {
	return &Broker{
		cash:      cash,
		positions: map[string]*position{},
		feeRate:   feeRate,
		slippage:  slippage,
		Now:       time.Now,
	}
}

// Sync 模拟盘的状态都在内存里，不需要同步。
func (b *Broker) Sync(context.Context) error { return nil }

func (b *Broker) MarketOrder(_ context.Context, symbol string, side model.Side, qty, refPrice decimal.Decimal) (model.Fill, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !qty.IsPositive() {
		return model.Fill{}, fmt.Errorf("下单数量必须大于 0，当前 %s", qty)
	}
	pos := b.positions[symbol]
	if pos == nil {
		pos = &position{}
		b.positions[symbol] = pos
	}

	fill := model.Fill{Symbol: symbol, Side: side, Qty: qty, Time: b.Now()}
	one := decimal.NewFromInt(1)
	switch side {
	case model.Buy:
		// 买入时价格往上滑
		fill.Price = refPrice.Mul(one.Add(b.slippage))
		notional := fill.Price.Mul(qty)
		fill.Fee = notional.Mul(b.feeRate)
		if notional.Add(fill.Fee).GreaterThan(b.cash) {
			return model.Fill{}, ErrInsufficientFunds
		}
		b.cash = b.cash.Sub(notional).Sub(fill.Fee)
		pos.qty = pos.qty.Add(qty)
		pos.costBasis = pos.costBasis.Add(notional).Add(fill.Fee)
	case model.Sell:
		// 只做多：不允许卖出超过持仓（不支持做空）
		if qty.GreaterThan(pos.qty) {
			return model.Fill{}, fmt.Errorf("卖出数量 %s 超过持仓 %s", qty, pos.qty)
		}
		fill.Price = refPrice.Mul(one.Sub(b.slippage))
		notional := fill.Price.Mul(qty)
		fill.Fee = notional.Mul(b.feeRate)
		// 按比例分摊成本
		cost := pos.costBasis.Mul(qty).Div(pos.qty)
		fill.RealizedPnL = notional.Sub(fill.Fee).Sub(cost)
		b.cash = b.cash.Add(notional).Sub(fill.Fee)
		pos.qty = pos.qty.Sub(qty)
		pos.costBasis = pos.costBasis.Sub(cost)
	default:
		return model.Fill{}, fmt.Errorf("未知方向 %q", side)
	}
	b.fills = append(b.fills, fill)
	return fill, nil
}

func (b *Broker) Position(symbol string) decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p := b.positions[symbol]; p != nil {
		return p.qty
	}
	return decimal.Zero
}

func (b *Broker) Cash() decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cash
}

func (b *Broker) Equity(marks map[string]decimal.Decimal) decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	eq := b.cash
	for sym, p := range b.positions {
		eq = eq.Add(p.qty.Mul(marks[sym]))
	}
	return eq
}

// Fills 返回所有成交记录的副本。
func (b *Broker) Fills() []model.Fill {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]model.Fill(nil), b.fills...)
}
