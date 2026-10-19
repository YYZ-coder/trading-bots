package strategy

import (
	"fmt"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

// SMACross 是均线交叉策略：
//   - 快线从下往上穿过慢线（金叉）→ 买入
//   - 快线从上往下穿过慢线（死叉）→ 卖出
//
// 这只是一个演示用的示例策略，不代表能赚钱。
type SMACross struct {
	fast, slow int
	closes     []decimal.Decimal
	prevDiff   *decimal.Decimal // 上一根 K 线的 (快线 - 慢线)
}

func NewSMACross(fast, slow int) (*SMACross, error) {
	if fast <= 0 || slow <= fast {
		return nil, fmt.Errorf("均线参数无效：需要 0 < fast < slow，当前 fast=%d slow=%d", fast, slow)
	}
	return &SMACross{fast: fast, slow: slow}, nil
}

func (s *SMACross) Name() string { return fmt.Sprintf("sma_cross(%d,%d)", s.fast, s.slow) }

// WarmupBars 返回策略开始产生信号前需要的 K 线数量。
func (s *SMACross) WarmupBars() int { return s.slow + 1 }

func (s *SMACross) OnCandle(c model.Candle) *model.Signal {
	s.closes = append(s.closes, c.Close)
	// 只保留慢线需要的数据，防止内存无限增长
	if len(s.closes) > s.slow {
		s.closes = s.closes[len(s.closes)-s.slow:]
	}
	if len(s.closes) < s.slow {
		return nil
	}

	diff := sma(s.closes[len(s.closes)-s.fast:]).Sub(sma(s.closes))
	prev := s.prevDiff
	s.prevDiff = &diff
	if prev == nil {
		return nil
	}
	switch {
	case !prev.IsPositive() && diff.IsPositive():
		return &model.Signal{Side: model.Buy, Reason: "金叉"}
	case !prev.IsNegative() && diff.IsNegative():
		return &model.Signal{Side: model.Sell, Reason: "死叉"}
	}
	return nil
}

func sma(xs []decimal.Decimal) decimal.Decimal {
	return decimal.Sum(xs[0], xs[1:]...).Div(decimal.NewFromInt(int64(len(xs))))
}
