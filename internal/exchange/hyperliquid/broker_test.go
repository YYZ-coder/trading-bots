package hyperliquid

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestRoundPrice(t *testing.T) {
	cases := []struct {
		px         string
		szDecimals int32
		want       string
	}{
		{"82412.37", 5, "82412"},     // 5 位有效数字 → 整数
		{"123456.7", 5, "123457"},    // 整数价格总是允许，即使超过 5 位有效数字
		{"3012.456", 4, "3012.5"},    // 5 位有效数字
		{"1.234567", 0, "1.2346"},    // 5 位有效数字
		{"0.0123456", 0, "0.012346"}, // 小数位上限 6 - 0 = 6
		{"0.0123456", 2, "0.0123"},   // 小数位上限 6 - 2 = 4
	}
	for _, c := range cases {
		got := roundPrice(decimal.RequireFromString(c.px), c.szDecimals).String()
		if got != c.want {
			t.Errorf("roundPrice(%s, %d) = %s, want %s", c.px, c.szDecimals, got, c.want)
		}
	}
}
