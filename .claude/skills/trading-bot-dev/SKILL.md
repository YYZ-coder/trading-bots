---
name: trading-bot-dev
description: 开发和维护本仓库的 Go 加密货币交易机器人（OKX 现货、Hyperliquid 永续、币安行情）。在新增策略、接入新交易所、修改下单/签名/风控、跑回测/模拟盘/测试网实盘、或排查交易所 API 问题时使用。Use when working on this Go trading bot: strategies, exchange adapters, order signing, risk, backtest/paper/live runs.
---

# Go 交易机器人开发指南

## 背景（决定了哪些事能做、哪些不能做）

- 币安国际站对美国、中国大陆、新加坡等多个地区不提供服务，所以本项目里**币安只用公开行情镜像做回测，不接实盘**。
- 实盘交易所：**OKX 现货**和 **Hyperliquid 永续**。
- 交易所的地区限制看的是居住地，不只是 IP。**不要建议用 VPN 绕过限制**；机器人应部署在和账户所在地一致的地区。新增交易所前先确认用户所在地区能合规使用。
- 和用户交流用**简体中文**，代码注释也用中文。

## 架构

```
K 线(MarketData) → Strategy(信号) → risk.Manager(数量) → Broker(下单)
```

- `internal/exchange/exchange.go`：`MarketData` 和 `Broker` 两个接口。回测、模拟盘、实盘共用 `internal/engine`，区别只在 K 线来源和 Broker。
- `Broker.Position/Cash/Equity` 读的是本地缓存；实盘 Broker 在 `Sync(ctx)` 里刷新。引擎在每根 K 线和每次成交后调用 `Sync`；同步失败时跳过这根 K 线，不交易。
- 策略只接收**已收盘**的 K 线。各交易所的 `SubscribeCandles` 负责过滤掉未收盘的；引擎按 `OpenTime` 去重。
- 只做多：卖出 = 平掉全部持仓。

### 新增策略
实现 `strategy.Strategy`（`Name()`、`OnCandle(model.Candle) *model.Signal`）。如果需要预热，再提供 `WarmupBars()`。然后在 `cmd/bot/main.go` 里接上参数。要写表驱动的单元测试，可以参考 `sma_cross_test.go`。

### 新增交易所
1. 新建 `internal/exchange/<name>/`，实现 `MarketData`：REST 历史 K 线要翻页；WebSocket 用 `exchange.KeepAlive` 自动重连，只推送已收盘的 K 线。
2. 如果要实盘，再实现 `Broker`：用 IOC 限价单模拟市价单，并加滑点保护；按交易所的精度截断数量和价格。
3. 在 `main.go` 的 `run()` 和 `newLiveBroker()` 里注册。
4. **先确认用户所在地区能合规使用这家交易所**，再写实盘代码。

交易所 API 的细节和已经踩过的坑，见 [references/exchange-notes.md](references/exchange-notes.md)。**改签名或下单代码前必须先读。**

## 必须遵守的安全规则

- 金额一律用 `shopspring/decimal`，**禁止 float64**。命令行参数例外，读进来后马上转成 decimal。
- 密钥只从 `.env` 或环境变量读取。**永远不要在输出、日志、测试或提交里打印私钥和 API Secret**；检查 `.env` 时只显示"已填写/空"。
- `-mode live` 默认用测试网或模拟盘。主网必须显式加 `-mainnet`，并保留 10 秒倒计时。不要削弱这层保护。
- 下单必须带滑点上限（`-max-slippage`）。Hyperliquid 的卖单必须是 `reduceOnly`。
- 风控（每单金额、最大持仓、当日亏损熔断）不能被绕过。熔断后仍然允许卖出平仓。
- OKX 的"持仓"是基础币的全部余额，要提醒用户使用**专用子账户**。Hyperliquid 推荐用 **API 钱包**（只能交易，不能提币）。
- 涉及真实资金的操作（主网运行、改主网默认值）先征得用户同意。

## 开发流程

```bash
gofmt -l . && go vet ./... && go test -race ./...   # 每次改动后必跑
go run ./cmd/bot -mode backtest -exchange okx -interval 1h -days 180
go run ./cmd/bot -mode paper -exchange hyperliquid -interval 1m -fast 3 -slow 5
HL_SIGN_CHECK=1 go test ./internal/exchange/hyperliquid -run Signature -v  # 联网验证签名
```

- **验证实时链路**：用 1m 周期加很短的均线（`-fast 3 -slow 5`），后台运行约 2 分钟，然后发 SIGINT，检查有没有收到已收盘的 K 线、有没有正常退出：
  `./bin/bot ... > /tmp/x.log 2>&1 & p=$!; perl -e 'sleep 130'; kill -INT $p; wait; cat /tmp/x.log`
- **跨交易所对照**：同一品种在 OKX 和币安上的回测结果应该很接近。如果差很多，先怀疑 K 线解析或翻页有问题。
- 实盘测试网验证时让用户用小额下单（`-order 20`）。Hyperliquid 的最小下单额约 10 美元。
- 提交信息用中文，`feat:`/`fix:` 前缀。不要提交 `.env`、`bin/`、`.claude/settings.local.json`。

## 当前状态和待办

见 [references/roadmap.md](references/roadmap.md)。完成一项就更新它。
