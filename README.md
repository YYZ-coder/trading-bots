# trading-bots

用 Go 写的加密货币交易机器人。

| 交易所 | 行情 / 回测 / 模拟盘 | 实盘 | 说明 |
|---|---|---|---|
| **OKX** 现货 | ✅ | ✅ 默认模拟盘（Demo Trading） | 按所在地区使用对应的 OKX 站点 |
| **Hyperliquid** 永续 | ✅ | ✅ 默认测试网 | 只做多，卖出用 reduceOnly 平仓 |
| 币安 现货 | ✅ | ❌ | 公开行情镜像，只用来回测 |

## 快速开始

```bash
# 回测
go run ./cmd/bot -mode backtest -exchange okx -symbol BTC-USDT -interval 1h -days 180
go run ./cmd/bot -mode backtest -exchange hyperliquid -symbol ETH -interval 4h -days 365

# 实时模拟盘（真实行情 + 虚拟账户，不需要 API Key）
go run ./cmd/bot -mode paper -exchange okx -interval 1m

# 实盘（默认测试网/模拟盘，需要先配置 .env）
cp .env.example .env   # 然后填写密钥
go run ./cmd/bot -mode live -exchange hyperliquid -symbol BTC -interval 15m
go run ./cmd/bot -mode live -exchange okx -symbol BTC-USDT -interval 15m

# 主网真实资金：必须显式加 -mainnet，启动后有 10 秒可以 Ctrl+C 取消
go run ./cmd/bot -mode live -exchange hyperliquid -mainnet ...

go run ./cmd/bot -h    # 所有参数
go test -race ./...    # 测试
HL_SIGN_CHECK=1 go test ./internal/exchange/hyperliquid -run Signature -v   # 联网验证 Hyperliquid 签名
```

## 配置测试账户

**Hyperliquid 测试网**
1. 在钱包（如 MetaMask）里**新建一个专用账户**，不要用存有真钱的钱包。
2. 打开 https://app.hyperliquid-testnet.xyz 连接钱包，在 Faucet 领测试 USDC。
3. 推荐：在 API 页面创建 **API 钱包**，把它的私钥填到 `HL_PRIVATE_KEY`，主账户地址填到 `HL_ACCOUNT_ADDRESS`。API 钱包只能交易，不能提币。

**OKX 模拟盘**
1. 登录 OKX → 交易 → 模拟交易，进入模拟盘。
2. 在模拟盘里创建 API Key（和实盘 Key 分开），权限只勾"交易"。
3. 把 Key、Secret、Passphrase 填到 `.env`。

## 安全设计

- 实盘默认走测试网/模拟盘；主网需要 `-mainnet`，并有 10 秒倒计时。
- 下单一律用 IOC 限价单，价格偏离参考价不超过 `-max-slippage`（默认 1%）。
- 风控：每单金额、单品种最大持仓、当日亏损熔断（熔断后只允许卖出）。
- 下单金额不超过可用余额，相当于不加杠杆。
- **OKX 的"持仓"是账户里基础币的全部余额**，卖出信号会全部卖掉。请给机器人单独开**子账户**。
- 程序退出时**不会自动平仓**。

## 目录结构

```
cmd/bot/                  入口、命令行参数
internal/model/           共享数据类型
internal/exchange/        统一接口、断线重连、周期解析
  okx/                    OKX 行情 + 签名请求 + 现货 Broker
  hyperliquid/            Hyperliquid 行情 + EIP-712 签名 + 永续 Broker
  binance/                币安行情（只读）
  paper/                  模拟盘 Broker
internal/strategy/        策略接口 + 均线交叉示例
internal/risk/            风控
internal/engine/          K 线 → 策略 → 风控 → 下单
internal/backtest/        回测和报告
```

## 已知限制

- 只做多，没有做空。Hyperliquid 的回测和模拟盘没有计算资金费率。
- 回测按收盘价加固定滑点成交，结果偏乐观。
- Hyperliquid 每个周期只保留最近 5000 根 K 线。
- Hyperliquid 实盘的手续费和已实现盈亏是估算值；OKX 实盘不显示单笔盈亏。
- WebSocket 断线重连期间如果正好有 K 线收盘，那一根会错过（不会补拉）。
