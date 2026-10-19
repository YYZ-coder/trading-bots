package risk

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func newManager() *Manager {
	return New(Config{
		OrderNotional:       d("1000"),
		MaxPositionNotional: d("1500"),
		MaxDailyLoss:        d("100"),
		QtyDecimals:         4,
	})
}

func TestBuySizing(t *testing.T) {
	m := newManager()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	buy := model.Signal{Side: model.Buy}

	qty, reason := m.Size(now, buy, d("100"), decimal.Zero, d("10000"), d("10000"))
	if reason != "" || !qty.Equal(d("10")) {
		t.Fatalf("qty=%s reason=%q, want 10", qty, reason)
	}
	// 已持仓 10（价值 1000），只剩 500 额度
	qty, _ = m.Size(now, buy, d("100"), d("10"), d("9000"), d("10000"))
	if !qty.Equal(d("5")) {
		t.Fatalf("qty=%s, want 5", qty)
	}
	// 仓位已满
	if qty, reason = m.Size(now, buy, d("100"), d("15"), d("8500"), d("10000")); !qty.IsZero() || reason == "" {
		t.Fatalf("仓位已满时应拒绝买入，qty=%s", qty)
	}
}

func TestDailyLossHalt(t *testing.T) {
	m := newManager()
	day1 := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	m.Observe(day1, d("10000"))
	m.Observe(day1.Add(time.Hour), d("9899"))
	if !m.Halted() {
		t.Fatal("亏损超过 100 应触发熔断")
	}
	if qty, _ := m.Size(day1.Add(2*time.Hour), model.Signal{Side: model.Buy}, d("100"), decimal.Zero, d("9899"), d("9899")); !qty.IsZero() {
		t.Fatal("熔断后不应允许买入")
	}
	if qty, _ := m.Size(day1.Add(2*time.Hour), model.Signal{Side: model.Sell}, d("100"), d("3"), d("9599"), d("9899")); !qty.Equal(d("3")) {
		t.Fatal("熔断后应允许卖出平仓")
	}
	// 第二天自动恢复
	m.Observe(day1.Add(24*time.Hour), d("9899"))
	if m.Halted() {
		t.Fatal("跨天后应解除熔断")
	}
}
