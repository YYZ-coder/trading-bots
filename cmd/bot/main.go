// 交易机器人入口。
//
// 用法示例：
//
//	go run ./cmd/bot -mode backtest -exchange okx -symbol BTC-USDT -interval 1h -days 180
//	go run ./cmd/bot -mode paper -exchange hyperliquid -symbol BTC -interval 1m
//	go run ./cmd/bot -mode live -exchange hyperliquid -symbol ETH -interval 15m   # 默认测试网
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/shopspring/decimal"

	"trading-bots/internal/backtest"
	"trading-bots/internal/engine"
	"trading-bots/internal/exchange"
	"trading-bots/internal/exchange/binance"
	"trading-bots/internal/exchange/hyperliquid"
	"trading-bots/internal/exchange/okx"
	"trading-bots/internal/exchange/paper"
	"trading-bots/internal/risk"
	"trading-bots/internal/strategy"
)

type options struct {
	mode, exchange, symbol, interval string
	days, fast, slow                 int
	cash, orderSize, maxPos, maxLoss float64
	fee, slippageBps, maxSlippage    float64
	mainnet, verbose                 bool
}

func main() {
	var o options
	flag.StringVar(&o.mode, "mode", "backtest", "运行模式：backtest（回测）| paper（实时模拟盘）| check（检查实盘配置）| live（实盘，默认测试网）")
	flag.StringVar(&o.exchange, "exchange", "okx", "交易所：okx | hyperliquid | binance（binance 只有行情，不能实盘）")
	flag.StringVar(&o.symbol, "symbol", "", "交易品种，默认 okx=BTC-USDT，hyperliquid=BTC，binance=BTCUSDT")
	flag.StringVar(&o.interval, "interval", "1h", "K 线周期，例如 1m 5m 15m 1h 4h 1d")
	flag.IntVar(&o.days, "days", 90, "回测天数")
	flag.IntVar(&o.fast, "fast", 10, "快线周期")
	flag.IntVar(&o.slow, "slow", 30, "慢线周期")
	flag.Float64Var(&o.cash, "cash", 10000, "回测/模拟盘的初始资金（实盘以账户余额为准）")
	flag.Float64Var(&o.orderSize, "order", 1000, "每次买入金额")
	flag.Float64Var(&o.maxPos, "max-pos", 5000, "单品种最大持仓金额")
	flag.Float64Var(&o.maxLoss, "max-daily-loss", 500, "当日最大亏损，触发后暂停开仓")
	flag.Float64Var(&o.fee, "fee", -1, "手续费率，默认 okx=0.001，hyperliquid=0.00045，binance=0.001")
	flag.Float64Var(&o.slippageBps, "slippage-bps", 5, "回测/模拟盘的模拟滑点（基点，1 bp = 0.01%）")
	flag.Float64Var(&o.maxSlippage, "max-slippage", 0.01, "实盘下单允许的最大滑点，0.01 = 1%")
	flag.BoolVar(&o.mainnet, "mainnet", false, "live/check 模式使用主网（默认用测试网/模拟盘）")
	flag.BoolVar(&o.verbose, "v", false, "输出调试日志")
	flag.Parse()

	level := slog.LevelInfo
	if o.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	// 从 .env 读取密钥（文件不存在时忽略，也可以直接用环境变量）
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Error("读取 .env 失败", "err", err)
		os.Exit(1)
	}

	// Ctrl+C 或 kill 时取消 ctx，让所有 goroutine 优雅退出
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, o, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("运行失败", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, log *slog.Logger) error {
	// 实盘和检查默认走测试网/模拟盘；回测和模拟盘始终用主网行情
	testnet := (o.mode == "live" || o.mode == "check") && !o.mainnet

	var md exchange.MarketData
	var defaultSymbol string
	var defaultFee float64
	switch o.exchange {
	case "okx":
		md, defaultSymbol, defaultFee = okx.New(okx.Config{
			BaseURL:    os.Getenv("OKX_BASE_URL"),
			Demo:       testnet,
			APIKey:     os.Getenv("OKX_API_KEY"),
			SecretKey:  os.Getenv("OKX_SECRET_KEY"),
			Passphrase: os.Getenv("OKX_PASSPHRASE"),
		}), "BTC-USDT", 0.001 // 现货吃单 0.1%
	case "hyperliquid":
		md, defaultSymbol, defaultFee = hyperliquid.New(testnet), "BTC", 0.00045 // 永续吃单 0.045%
	case "binance":
		md, defaultSymbol, defaultFee = binance.New(), "BTCUSDT", 0.001
	default:
		return fmt.Errorf("不支持的交易所 %q", o.exchange)
	}
	if o.symbol == "" {
		o.symbol = defaultSymbol
	}
	if o.fee < 0 {
		o.fee = defaultFee
	}
	barDur, err := exchange.ParseInterval(o.interval)
	if err != nil {
		return err
	}

	strat, err := strategy.NewSMACross(o.fast, o.slow)
	if err != nil {
		return err
	}
	rm := risk.New(risk.Config{
		OrderNotional:       decimal.NewFromFloat(o.orderSize),
		MaxPositionNotional: decimal.NewFromFloat(o.maxPos),
		MaxDailyLoss:        decimal.NewFromFloat(o.maxLoss),
		QtyDecimals:         8, // 交易所 Broker 会再按各自的精度取整
	})
	log = log.With("exchange", md.Name(), "symbol", o.symbol)
	newPaperBroker := func() *paper.Broker {
		return paper.New(
			decimal.NewFromFloat(o.cash),
			decimal.NewFromFloat(o.fee),
			decimal.NewFromFloat(o.slippageBps).Div(decimal.NewFromInt(10000)),
		)
	}

	switch o.mode {
	case "backtest":
		end := time.Now()
		start := end.AddDate(0, 0, -o.days)
		log.Info("下载历史 K 线", "interval", o.interval, "from", start.Format(time.DateOnly))
		candles, err := md.Candles(ctx, o.symbol, o.interval, start, end)
		if err != nil {
			return err
		}
		if expected := int(end.Sub(start) / barDur); len(candles) < expected*9/10 {
			log.Warn("拿到的 K 线比预期少（Hyperliquid 每个周期只保留最近 5000 根）",
				"got", len(candles), "expected", expected)
		}
		rep, err := backtest.Run(ctx, o.symbol, candles, strat, newPaperBroker(), rm, log)
		if err != nil {
			return err
		}
		rep.Print(os.Stdout)
		return nil

	case "paper":
		return runRealtime(ctx, o, md, barDur, strat, newPaperBroker(), rm, log)

	case "live":
		broker, err := newLiveBroker(ctx, o, md)
		if err != nil {
			return err
		}
		if o.mainnet {
			log.Warn("⚠️  主网实盘：将使用真实资金交易，10 秒后开始，按 Ctrl+C 取消")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
			}
		} else {
			log.Info("实盘模式（测试网/模拟盘，不涉及真实资金）")
		}
		return runRealtime(ctx, o, md, barDur, strat, broker, rm, log)

	case "check":
		return runCheck(ctx, o, md, barDur)

	default:
		return fmt.Errorf("未知模式 %q", o.mode)
	}
}

func newLiveBroker(ctx context.Context, o options, md exchange.MarketData) (exchange.Broker, error) {
	slippage := decimal.NewFromFloat(o.maxSlippage)
	switch c := md.(type) {
	case *hyperliquid.Client:
		key := os.Getenv("HL_PRIVATE_KEY")
		if key == "" {
			return nil, errors.New("缺少 HL_PRIVATE_KEY：请在 .env 里设置签名用的私钥（推荐用 API 钱包）")
		}
		b, err := hyperliquid.NewBroker(ctx, c, key, os.Getenv("HL_ACCOUNT_ADDRESS"), slippage, decimal.NewFromFloat(o.fee))
		if err != nil {
			return nil, err
		}
		slog.Info("Hyperliquid 账户", "address", b.Account().Hex(), "equity", b.Equity(nil), "withdrawable", b.Cash(),
			"position", b.Position(o.symbol))
		return b, nil
	case *okx.Client:
		b, err := okx.NewBroker(ctx, c, o.symbol, slippage)
		if err != nil {
			return nil, err
		}
		slog.Info("OKX 账户", "cash", b.Cash(), "position", b.Position(o.symbol))
		return b, nil
	default:
		return nil, fmt.Errorf("%s 不支持实盘交易（只提供行情数据）", md.Name())
	}
}

// runRealtime 用实时行情驱动引擎。模拟盘和实盘共用，区别只在 broker。
func runRealtime(ctx context.Context, o options, md exchange.MarketData, barDur time.Duration,
	strat *strategy.SMACross, broker exchange.Broker, rm *risk.Manager, log *slog.Logger) error {
	// 先订阅再拉历史，避免两者之间漏掉一根 K 线（引擎会自动去重）
	liveCandles, err := md.SubscribeCandles(ctx, o.symbol, o.interval)
	if err != nil {
		return err
	}
	end := time.Now()
	history, err := md.Candles(ctx, o.symbol, o.interval, end.Add(-barDur*time.Duration(strat.WarmupBars()+5)), end)
	if err != nil {
		return err
	}

	eng := engine.New(o.symbol, strat, broker, rm, log)
	eng.Warmup(history)
	log.Info("已启动，等待 K 线收盘（Ctrl+C 退出）",
		"mode", o.mode, "strategy", strat.Name(), "interval", o.interval, "warmup_bars", len(history))

	err = eng.Run(ctx, liveCandles)
	s := eng.Stats()
	log.Info("已停止（持仓不会自动平掉）", "candles", s.Candles, "start_equity", s.StartEquity.StringFixed(2),
		"equity", s.LastEquity.StringFixed(2), "position", broker.Position(o.symbol))
	return err
}
