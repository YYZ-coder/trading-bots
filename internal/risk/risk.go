// Package risk 风控：决定每个信号能下多少，以及什么时候必须停止交易。
package risk

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

type Config struct {
	// OrderNotional 每次买入的金额（计价货币，例如 USDT）。
	OrderNotional decimal.Decimal
	// MaxPositionNotional 单个品种的最大持仓金额。
	MaxPositionNotional decimal.Decimal
	// MaxDailyLoss 当日亏损上限（相对当日开始时的权益）。触发后当天只允许卖出平仓。
	MaxDailyLoss decimal.Decimal
	// QtyDecimals 下单数量保留的小数位。
	QtyDecimals int32
}

type Manager struct {
	cfg            Config
	day            time.Time // 当前交易日（UTC 0 点）
	dayStartEquity decimal.Decimal
	halted         bool
}

func New(cfg Config) *Manager { return &Manager{cfg: cfg} }

// Halted 表示当日亏损已触发熔断。
func (m *Manager) Halted() bool { return m.halted }

// Size 根据信号、当前价格和账户状态计算下单数量。
// 返回 0 和原因表示这个信号被拒绝。
func (m *Manager) Size(now time.Time, sig model.Signal, price, position, cash, equity decimal.Decimal) (decimal.Decimal, string) {
	m.updateDay(now, equity)

	switch sig.Side {
	case model.Sell:
		// 卖出（平仓）永远允许，用于止损
		if !position.IsPositive() {
			return decimal.Zero, "没有持仓"
		}
		return position, ""

	case model.Buy:
		if m.halted {
			return decimal.Zero, "当日亏损已达上限，暂停开仓"
		}
		room := m.cfg.MaxPositionNotional.Sub(position.Mul(price))
		// 预留 1% 给手续费和滑点
		notional := decimal.Min(m.cfg.OrderNotional, room, cash.Mul(decimal.NewFromFloat(0.99)))
		qty := notional.Div(price).Truncate(m.cfg.QtyDecimals)
		if !qty.IsPositive() {
			return decimal.Zero, fmt.Sprintf("可用额度不足（剩余仓位额度 %s，现金 %s）", room.StringFixed(2), cash.StringFixed(2))
		}
		return qty, ""
	}
	return decimal.Zero, fmt.Sprintf("未知方向 %q", sig.Side)
}

// Observe 在每根 K 线后更新风控状态（检查是否触发当日亏损熔断）。
func (m *Manager) Observe(now time.Time, equity decimal.Decimal) {
	m.updateDay(now, equity)
	if !m.halted && m.dayStartEquity.Sub(equity).GreaterThanOrEqual(m.cfg.MaxDailyLoss) {
		m.halted = true
	}
}

// updateDay 在跨过 UTC 0 点时重置当日统计。
func (m *Manager) updateDay(now time.Time, equity decimal.Decimal) {
	day := now.UTC().Truncate(24 * time.Hour)
	if !day.Equal(m.day) {
		m.day = day
		m.dayStartEquity = equity
		m.halted = false
	}
}
