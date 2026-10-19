// Package exchange 定义交易所适配层的统一接口。
// 每个交易所（binance、hyperliquid）和模拟盘（paper）各自实现这些接口，
// 这样策略和引擎代码完全不需要关心底层是哪家交易所。
package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

// MarketData 提供历史和实时 K 线。
type MarketData interface {
	Name() string
	// Candles 返回 [start, end) 区间内已收盘的 K 线，按时间升序。
	Candles(ctx context.Context, symbol, interval string, start, end time.Time) ([]model.Candle, error)
	// SubscribeCandles 订阅实时 K 线，只推送已收盘的 K 线。
	// 断线会自动重连；ctx 取消后 channel 会被关闭。
	SubscribeCandles(ctx context.Context, symbol, interval string) (<-chan model.Candle, error)
}

// Broker 负责下单和查询账户。实盘和模拟盘都实现这个接口。
//
// Position / Cash / Equity 返回的是本地缓存的账户状态，调用很快、不发网络请求；
// 实盘 Broker 需要先调用 Sync 从交易所刷新缓存。
type Broker interface {
	// Sync 从交易所拉取最新的余额和持仓。模拟盘直接返回 nil。
	Sync(ctx context.Context) error
	// MarketOrder 按市价下单。refPrice 是当前参考价：
	// 模拟盘用它计算成交价，实盘可以用它设置滑点保护。
	MarketOrder(ctx context.Context, symbol string, side model.Side, qty, refPrice decimal.Decimal) (model.Fill, error)
	Position(symbol string) decimal.Decimal
	Cash() decimal.Decimal
	// Equity 返回按 marks（symbol → 最新价）估值的账户总权益。
	Equity(marks map[string]decimal.Decimal) decimal.Decimal
}
