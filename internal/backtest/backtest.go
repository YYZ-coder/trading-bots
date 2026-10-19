// Package backtest 用历史 K 线回放来测试策略。
package backtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/engine"
	"trading-bots/internal/exchange/paper"
	"trading-bots/internal/model"
	"trading-bots/internal/risk"
	"trading-bots/internal/strategy"
)

type Report struct {
	Strategy      string
	Symbol        string
	From, To      time.Time
	Stats         engine.Stats
	Return        decimal.Decimal // 策略收益率
	BuyHoldReturn decimal.Decimal // 同期"买入并持有"的收益率，用来对比
	RoundTrips    int             // 完整的买入→卖出次数
	Wins          int
	TotalFees     decimal.Decimal
	RealizedPnL   decimal.Decimal
	OpenPosition  decimal.Decimal
}

// Run 按时间顺序把 candles 喂给引擎，所有成交都在 broker（模拟盘）里记账。
func Run(ctx context.Context, symbol string, candles []model.Candle, s strategy.Strategy, broker *paper.Broker, r *risk.Manager, log *slog.Logger) (Report, error) {
	if len(candles) < 2 {
		return Report{}, errors.New("K 线数量太少，无法回测")
	}

	// 让模拟盘的成交时间等于当前 K 线的收盘时间，而不是电脑的当前时间
	var now time.Time
	broker.Now = func() time.Time { return now }

	eng := engine.New(symbol, s, broker, r, log)
	for _, c := range candles {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		now = c.CloseTime
		eng.OnCandle(ctx, c)
	}

	first, last := candles[0], candles[len(candles)-1]
	rep := Report{
		Strategy:      s.Name(),
		Symbol:        symbol,
		From:          first.OpenTime,
		To:            last.CloseTime,
		Stats:         eng.Stats(),
		BuyHoldReturn: last.Close.Sub(first.Close).Div(first.Close),
		OpenPosition:  broker.Position(symbol),
	}
	if rep.Stats.StartEquity.IsPositive() {
		rep.Return = rep.Stats.LastEquity.Sub(rep.Stats.StartEquity).Div(rep.Stats.StartEquity)
	}
	for _, f := range broker.Fills() {
		rep.TotalFees = rep.TotalFees.Add(f.Fee)
		if f.Side == model.Sell {
			rep.RoundTrips++
			rep.RealizedPnL = rep.RealizedPnL.Add(f.RealizedPnL)
			if f.RealizedPnL.IsPositive() {
				rep.Wins++
			}
		}
	}
	return rep, nil
}

func pct(d decimal.Decimal) string { return d.Mul(decimal.NewFromInt(100)).StringFixed(2) + "%" }

func (r Report) Print(w io.Writer) {
	winRate := "-"
	if r.RoundTrips > 0 {
		winRate = pct(decimal.NewFromInt(int64(r.Wins)).Div(decimal.NewFromInt(int64(r.RoundTrips))))
	}
	fmt.Fprintf(w, `
========== 回测报告 ==========
策略        %s
品种        %s
区间        %s → %s（%d 根 K 线）
初始权益    %s
最终权益    %s
策略收益    %s
买入持有    %s
最大回撤    %s
交易次数    %d（胜率 %s）
已实现盈亏  %s
手续费合计  %s
未平仓数量  %s
==============================
`,
		r.Strategy, r.Symbol,
		r.From.UTC().Format(time.DateOnly), r.To.UTC().Format(time.DateOnly), r.Stats.Candles,
		r.Stats.StartEquity.StringFixed(2), r.Stats.LastEquity.StringFixed(2),
		pct(r.Return), pct(r.BuyHoldReturn), pct(r.Stats.MaxDrawdown),
		r.RoundTrips, winRate,
		r.RealizedPnL.StringFixed(2), r.TotalFees.StringFixed(2), r.OpenPosition,
	)
}
