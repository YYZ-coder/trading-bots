# 当前状态和待办

## 已完成
- [x] 币安、OKX、Hyperliquid 的行情（REST 历史 + WebSocket 实时）
- [x] 回测引擎和报告，模拟盘 Broker
- [x] 风控：每单金额、最大持仓、当日亏损熔断
- [x] OKX 现货实盘 Broker（默认模拟盘）
- [x] Hyperliquid 永续实盘 Broker（默认测试网），签名已在测试网验证

## 待用户配合
- [ ] 用户创建 Hyperliquid 测试网专用钱包 / API 钱包并领测试币，填 `.env`
- [ ] 用户创建 OKX 模拟盘 API Key（只开交易权限），填 `.env`
- [ ] 用 `-mode check` 确认配置，然后用 `-order 20` 在测试网实际成交一笔，检查成交解析、手续费和平仓

## 待开发
- [ ] WebSocket 重连后用 REST 补拉错过的 K 线
- [ ] Telegram 告警（成交、熔断、断线、报错）
- [ ] 部署：Dockerfile / systemd，部署到和账户所在地一致的 VPS
- [ ] 回测：资金费率（Hyperliquid）、参数扫描
