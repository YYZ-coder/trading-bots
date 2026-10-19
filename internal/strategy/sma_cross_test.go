package strategy

import (
	"testing"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

func TestSMACrossSignals(t *testing.T) {
	s, err := NewSMACross(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 先下跌再上涨再下跌，应依次出现一次金叉和一次死叉
	prices := []int64{10, 9, 8, 7, 9, 12, 14, 10, 6, 4}
	var got []model.Side
	for _, p := range prices {
		if sig := s.OnCandle(model.Candle{Close: decimal.NewFromInt(p)}); sig != nil {
			got = append(got, sig.Side)
		}
	}
	want := []model.Side{model.Buy, model.Sell}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("signals = %v, want %v", got, want)
	}
}

func TestSMACrossInvalidParams(t *testing.T) {
	if _, err := NewSMACross(5, 5); err == nil {
		t.Fatal("fast >= slow 应该报错")
	}
}
