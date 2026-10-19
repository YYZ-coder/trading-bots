# 交易所 API 笔记（都已在本项目里实测过）

## 网络可达性（2026-10 实测）

| 端点 | 状态 |
|---|---|
| `api.binance.com`、`testnet.binance.vision` | ❌ 返回 "restricted location"（地区限制，不要绕过） |
| `data-api.binance.vision`、`wss://data-stream.binance.vision` | ✅ 公开行情镜像，不需要账号 |
| `www.okx.com`（`my.okx.com`、`app.okx.com` 也能用） | ✅ |
| `api.hyperliquid.xyz`、`api.hyperliquid-testnet.xyz` | ✅ |
| Go 模块代理 | `goproxy.cn` |

## 币安（只读）

- REST `GET /api/v3/klines`：每次最多 1000 根。每一行是混合类型的数组 `[openTime, "o","h","l","c","v", closeTime, ...]`，用 `[]json.RawMessage` 逐个字段解析。
- WebSocket `wss://data-stream.binance.vision/ws/<symbol小写>@kline_<interval>`：推送里的 `k.x == true` 表示这根 K 线已收盘。币安每 3 分钟发一次 ping，`coder/websocket` 在 Read 时会自动回 pong。

## OKX（API v5）

- **周期写法**：`1m`、`1H`、`4H`。6 小时、12 小时、日线、周线要用 `6Hutc`、`12Hutc`、`1Dutc`、`1Wutc`，否则 OKX 按香港时间（UTC+8）切分 K 线。见 `okxBar()`。
- **历史 K 线** `GET /api/v5/market/history-candles`：
  - 每次最多 100 根，限频 20 次/2 秒，所以请求之间要 sleep 120ms。
  - 返回结果**按时间倒序**（最新在前）。用 `after=<ts>` 往更早翻页。
  - 每一行是 `[ts,o,h,l,c,vol,volCcy,volCcyQuote,confirm]`，`confirm=="1"` 表示已收盘。
- **WebSocket**：K 线在 **business** 端点上，不在 public 端点上。
  - 实盘 `wss://ws.okx.com:8443/ws/v5/business`，模拟盘 `wss://wspap.okx.com:8443/ws/v5/business`。
  - 订阅格式 `{"op":"subscribe","args":[{"channel":"candle1m","instId":"BTC-USDT"}]}`。
  - 心跳：每 20 秒发纯文本 `"ping"`，服务器回 `"pong"`（不是 JSON，解析前要先判断）。
- **签名**：`Base64(HMAC-SHA256(secret, timestamp + METHOD + requestPath(含 ?query) + body))`。
  - 时间戳格式 `2006-01-02T15:04:05.000Z`（UTC）。
  - 请求头：`OK-ACCESS-KEY`、`OK-ACCESS-SIGN`、`OK-ACCESS-TIMESTAMP`、`OK-ACCESS-PASSPHRASE`。
- **模拟盘**：域名和实盘相同，加请求头 `x-simulated-trading: 1`。API Key 要在模拟盘里**单独创建**。
- **数字字段经常是空字符串 `""`**，直接反序列化成 `decimal.Decimal` 会报错。先读成 string，再用 `dec()` 转换（空字符串当 0）。
- **错误**：外层 `code != "0"` 表示失败，下单类接口的具体原因在 `data[0].sCode/sMsg` 里。`50111` = API Key 无效。
- **下单**：`POST /api/v5/trade/order`，参数 `tdMode=cash, ordType=ioc, px, sz`（sz 是基础币数量）。
  - 下单后轮询 `GET /api/v5/trade/order`，直到 state 变成 `filled`、`canceled` 或 `mmp_canceled`。
  - `fee` 是负数。买入时手续费按基础币扣（`feeCcy == baseCcy`），要乘成交均价换算成计价货币。
- 精度来自 `GET /api/v5/public/instruments`：`lotSz`（数量步长）、`minSz`（最小下单量）、`tickSz`（价格步长）。

## Hyperliquid

- **行情**：`POST /info {"type":"candleSnapshot",...}`，每个周期**只保留最近 5000 根**。
- **WebSocket** `wss://api.hyperliquid.xyz/ws`：
  - 推送里的 `data` 是单个对象，**没有收盘标记**。两种情况视为收盘，以先到的为准：收到下一根 K 线的更新；或者过了 `T`（收盘时间）2 秒仍没有新更新。
  - 只靠"等下一根更新"是不行的：测试网成交稀少，实测 75 秒内都等不到，信号会严重延迟。所以必须加计时器。
  - 60 秒内没有消息服务器会断开连接，所以每 30 秒发一次 `{"method":"ping"}`。
- **账户**：`{"type":"clearinghouseState","user":addr}`。
  - `marginSummary.accountValue` = 总权益，`withdrawable` = 可用资金。
  - 持仓在 `assetPositions[].position{coin,szi,entryPx}`，`szi` 是负数表示空头。
- **币种下标**：`{"type":"meta"}` 返回的 `universe` 数组里的位置，就是下单时用的 asset index，`szDecimals` 是数量精度。
- **L1 签名**（`signing.go`，和官方 Python SDK 一致）：
  1. `connectionId = keccak256(msgpack(action) || nonce 8字节大端 || 0x00)`，最后的 0x00 表示没有 vault。
  2. EIP-712 结构体 `Agent(string source,bytes32 connectionId)`。主网 `source="a"`，测试网 `"b"`。
  3. domain：`name="Exchange", version="1", chainId=1337, verifyingContract=0x0`。
  4. 签名格式 `{r,s: 0x 开头的 hex, v: 27/28}`。
- **msgpack 的坑**：
  - action 必须用**带 msgpack tag 的结构体**，字段顺序和官方完全一致：`type, orders, grouping`；每个订单是 `a, b, p, s, r, t`。不能用 map，因为 map 遍历顺序随机，签名会对不上。
  - 整数字段用 Go 的 `int`。`vmihailenco/msgpack/v5` 会把非负数编码成最紧凑的 uint 格式，和 Python 一致。这一点已经用下标 > 127 的币种验证过。
- **验证签名的技巧**：用随机生成的空钱包往测试网下单，交易所会返回 `User or API Wallet 0x... does not exist.`。这个地址是**从签名反推出来的**，如果和你的地址一致，签名就是对的。见 `signcheck_test.go`。
- **价格规则**：最多 5 位有效数字，且小数位不超过 `6 - szDecimals`。整数价格总是允许。见 `roundPrice()`。数量按 `szDecimals` 截断。p/s 字段是去掉末尾零的字符串（`decimal.String()` 正好是这个格式）。
- **nonce**：毫秒时间戳，必须严格递增，见 `nextNonce()`。
- **市价单** = `{"limit":{"tif":"Ioc"}}`，价格是参考价 ±滑点。下单响应里的 `statuses[0]` 是 `filled{totalSz,avgPx,oid}` 或 `error`。
- **API 钱包**：用 API 钱包的私钥签名，查余额用主账户地址（`HL_ACCOUNT_ADDRESS`）。
- 最小下单金额约 10 美元。据了解，测试网 Faucet 可能要求这个地址在主网上入过金。
