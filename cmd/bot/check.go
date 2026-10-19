package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/exchange"
	"trading-bots/internal/exchange/hyperliquid"
	"trading-bots/internal/exchange/okx"
)

// requiredEnv 列出每个交易所实盘需要的环境变量。
var requiredEnv = map[string][]string{
	"hyperliquid": {"HL_PRIVATE_KEY"},
	"okx":         {"OKX_API_KEY", "OKX_SECRET_KEY", "OKX_PASSPHRASE"},
}

type checklist struct{ failed int }

func (c *checklist) pass(name, detail string) { fmt.Printf("  ✅ %s %s\n", pad(name, 14), detail) }
func (c *checklist) warn(name, detail string) { fmt.Printf("  ⚠️  %s %s\n", pad(name, 14), detail) }
func (c *checklist) fail(name, detail string) {
	c.failed++
	fmt.Printf("  ❌ %s %s\n", pad(name, 14), detail)
}

// pad 按终端显示宽度补空格（中文字符占两列），让检查结果对齐。
func pad(s string, width int) string {
	w := 0
	for _, r := range s {
		if r >= 0x2E80 {
			w += 2
		} else {
			w++
		}
	}
	return s + strings.Repeat(" ", max(width-w, 0))
}

// runCheck 检查实盘所需的一切是否就绪：密钥、行情、账户、签名授权。不会产生任何成交。
func runCheck(ctx context.Context, o options, md exchange.MarketData, barDur time.Duration) error {
	env := "测试网/模拟盘"
	if o.mainnet {
		env = "主网（只读检查）"
	}
	fmt.Printf("\n检查 %s %s（%s）\n\n", md.Name(), o.symbol, env)
	var c checklist

	// WebSocket 要等一根 1 分钟 K 线收盘，比较慢，先在后台开始
	wsCtx, cancelWS := context.WithTimeout(ctx, 75*time.Second)
	defer cancelWS()
	wsStart := time.Now()
	wsCandles, wsErr := md.SubscribeCandles(wsCtx, o.symbol, "1m")

	// 1. 行情 REST
	var refPrice decimal.Decimal
	end := time.Now()
	candles, err := md.Candles(ctx, o.symbol, o.interval, end.Add(-10*barDur), end)
	switch {
	case err != nil:
		c.fail("行情 REST", err.Error())
	case len(candles) == 0:
		c.fail("行情 REST", "没有返回 K 线，请检查品种名称")
	default:
		refPrice = candles[len(candles)-1].Close
		c.pass("行情 REST", fmt.Sprintf("%d 根 %s K 线，最新收盘价 %s", len(candles), o.interval, refPrice))
	}

	// 2. 密钥（只显示是否填写，绝不显示内容）
	keysOK := true
	for _, k := range requiredEnv[o.exchange] {
		if os.Getenv(k) == "" {
			keysOK = false
			c.fail("密钥", k+" 未填写（编辑 .env）")
		}
	}
	if keysOK {
		c.pass("密钥", "已填写")
	}

	// 3. 账户和签名授权
	if keysOK {
		checkAccount(ctx, o, md, refPrice, &c)
	}

	// 4. 行情 WebSocket
	if wsErr != nil {
		c.fail("行情 WebSocket", wsErr.Error())
	} else {
		fmt.Println("  …  等待一根 1 分钟 K 线收盘（最多 75 秒）")
		select {
		case k, ok := <-wsCandles:
			if ok {
				c.pass("行情 WebSocket", fmt.Sprintf("收到 %s 收盘的 K 线（等待 %s）",
					k.OpenTime.Format("15:04"), time.Since(wsStart).Round(time.Second)))
			} else {
				c.warn("行情 WebSocket", "75 秒内没有收到收盘 K 线（测试网成交稀少时可能发生，可以重试）")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	fmt.Println()
	if c.failed > 0 {
		return fmt.Errorf("%d 项检查未通过", c.failed)
	}
	fmt.Println("全部检查通过，可以运行 -mode live。")
	return nil
}

func checkAccount(ctx context.Context, o options, md exchange.MarketData, refPrice decimal.Decimal, c *checklist) {
	broker, err := newLiveBroker(ctx, o, md)
	if err != nil {
		c.fail("账户", err.Error())
		return
	}
	cash := broker.Cash()
	c.pass("账户", fmt.Sprintf("可用资金 %s，%s 持仓 %s", cash.StringFixed(2), o.symbol, broker.Position(o.symbol)))

	minOrder := decimal.NewFromInt(10) // Hyperliquid 最小下单额
	if b, ok := broker.(*okx.Broker); ok {
		minOrder = b.MinNotional(refPrice)
	}
	order := decimal.NewFromFloat(o.orderSize)
	switch {
	case cash.LessThan(minOrder):
		c.fail("资金", fmt.Sprintf("可用资金 %s 低于最小下单额 %s，请先充值或领测试币", cash.StringFixed(2), minOrder.StringFixed(2)))
	case order.LessThan(minOrder):
		c.fail("资金", fmt.Sprintf("-order %s 低于最小下单额 %s", order, minOrder.StringFixed(2)))
	case cash.LessThan(order):
		c.warn("资金", fmt.Sprintf("可用资金 %s 少于 -order %s，实际下单会按可用资金缩小", cash.StringFixed(2), order))
	default:
		c.pass("资金", fmt.Sprintf("足够下单（-order %s，最小下单额 %s）", order, minOrder.StringFixed(2)))
	}

	switch b := broker.(type) {
	case *hyperliquid.Broker:
		if o.mainnet || refPrice.IsZero() {
			c.warn("签名授权", "主网只做只读检查，跳过下单授权验证")
			return
		}
		if err := b.CheckAuth(ctx, o.symbol, refPrice); err != nil {
			c.fail("签名授权", err.Error())
		} else {
			c.pass("签名授权", "测试单被交易所接受（价格远低于市价，不会成交）")
		}
	case *okx.Broker:
		// 能查到余额就说明 API Key 和签名都正确
		c.pass("签名授权", "带签名的余额查询成功")
	}
}
