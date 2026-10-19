package paper

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestBuySellPnL(t *testing.T) {
	ctx := context.Background()
	b := New(d("1000"), d("0.001"), decimal.Zero)

	if _, err := b.MarketOrder(ctx, "BTC", model.Buy, d("1"), d("100")); err != nil {
		t.Fatal(err)
	}
	// 买入 100 + 手续费 0.1
	if got := b.Cash(); !got.Equal(d("899.9")) {
		t.Fatalf("cash = %s, want 899.9", got)
	}

	fill, err := b.MarketOrder(ctx, "BTC", model.Sell, d("1"), d("110"))
	if err != nil {
		t.Fatal(err)
	}
	// 卖出 110 - 手续费 0.11 - 成本 100.1 = 9.79
	if !fill.RealizedPnL.Equal(d("9.79")) {
		t.Fatalf("pnl = %s, want 9.79", fill.RealizedPnL)
	}
	if got := b.Cash(); !got.Equal(d("1009.79")) {
		t.Fatalf("cash = %s, want 1009.79", got)
	}
	if !b.Position("BTC").IsZero() {
		t.Fatalf("position = %s, want 0", b.Position("BTC"))
	}
}

func TestSlippageAndEquity(t *testing.T) {
	b := New(d("1000"), decimal.Zero, d("0.01"))
	fill, err := b.MarketOrder(context.Background(), "BTC", model.Buy, d("2"), d("100"))
	if err != nil {
		t.Fatal(err)
	}
	if !fill.Price.Equal(d("101")) {
		t.Fatalf("buy price = %s, want 101", fill.Price)
	}
	// 现金 798 + 持仓 2 * 120
	if got := b.Equity(map[string]decimal.Decimal{"BTC": d("120")}); !got.Equal(d("1038")) {
		t.Fatalf("equity = %s, want 1038", got)
	}
}

func TestRejects(t *testing.T) {
	ctx := context.Background()
	b := New(d("100"), d("0.001"), decimal.Zero)
	if _, err := b.MarketOrder(ctx, "BTC", model.Buy, d("1"), d("100")); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
	if _, err := b.MarketOrder(ctx, "BTC", model.Sell, d("1"), d("100")); err == nil {
		t.Fatal("卖出超过持仓应该报错")
	}
}
