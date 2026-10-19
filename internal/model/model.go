// Package model 定义各模块之间共享的基础数据类型。
package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// Candle 是一根 K 线。所有价格和数量都用 decimal，避免 float64 的精度误差。
type Candle struct {
	Symbol    string
	Interval  string
	OpenTime  time.Time
	CloseTime time.Time
	Open      decimal.Decimal
	High      decimal.Decimal
	Low       decimal.Decimal
	Close     decimal.Decimal
	Volume    decimal.Decimal
}

type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

// Signal 是策略给出的交易信号。策略只负责"想买/想卖"，下多少由风控决定。
type Signal struct {
	Side   Side
	Reason string
}

// Fill 是一笔成交记录。
type Fill struct {
	Symbol string
	Side   Side
	Qty    decimal.Decimal
	Price  decimal.Decimal
	Fee    decimal.Decimal
	Time   time.Time
	// RealizedPnL 只在卖出时有值（已扣除买卖双边手续费）。
	RealizedPnL decimal.Decimal
}
