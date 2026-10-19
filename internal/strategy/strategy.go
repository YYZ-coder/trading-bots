// Package strategy 存放交易策略。策略只看行情、给信号，不碰账户和下单。
package strategy

import "trading-bots/internal/model"

type Strategy interface {
	Name() string
	// OnCandle 在每根 K 线收盘时调用。返回 nil 表示不操作。
	OnCandle(c model.Candle) *model.Signal
}
