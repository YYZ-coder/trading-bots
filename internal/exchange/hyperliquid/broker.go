package hyperliquid

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

// Broker 是 Hyperliquid 永续合约的实盘 Broker。
//
// 只做多：买入开多，卖出时用 reduceOnly 平仓，不会意外开空。
// 支持 API 钱包（Agent Wallet）：用 API 钱包的私钥签名，用主账户地址查询余额。
// API 钱包只能交易、不能提币，比直接用主钱包私钥安全得多。
type Broker struct {
	c        *Client
	key      *ecdsa.PrivateKey
	account  common.Address
	slippage decimal.Decimal // 市价单允许的最大滑点，例如 0.01 = 1%
	feeRate  decimal.Decimal // 只用于估算手续费，实际手续费以交易所为准

	assets map[string]asset

	mu        sync.Mutex
	lastNonce int64
	equity    decimal.Decimal
	cash      decimal.Decimal
	positions map[string]position
}

type asset struct {
	index      int
	szDecimals int32
}

type position struct {
	size    decimal.Decimal // 正数为多，负数为空
	entryPx decimal.Decimal
}

// NewBroker 创建实盘 Broker。privateKeyHex 是签名用的私钥（主钱包或 API 钱包），
// account 是主账户地址；为空时使用私钥对应的地址。
func NewBroker(ctx context.Context, c *Client, privateKeyHex, account string, slippage, feeRate decimal.Decimal) (*Broker, error) {
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return nil, fmt.Errorf("私钥格式错误: %w", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	if account != "" {
		if !common.IsHexAddress(account) {
			return nil, fmt.Errorf("账户地址格式错误: %q", account)
		}
		addr = common.HexToAddress(account)
	}
	b := &Broker{
		c: c, key: key, account: addr,
		slippage: slippage, feeRate: feeRate,
		positions: map[string]position{},
	}
	if err := b.loadMeta(ctx); err != nil {
		return nil, err
	}
	return b, b.Sync(ctx)
}

// Account 返回查询余额用的主账户地址。
func (b *Broker) Account() common.Address { return b.account }

// loadMeta 加载永续合约列表。下单时用的是币种在列表里的下标（asset index）。
func (b *Broker) loadMeta(ctx context.Context) error {
	var meta struct {
		Universe []struct {
			Name       string `json:"name"`
			SzDecimals int32  `json:"szDecimals"`
		} `json:"universe"`
	}
	if err := b.c.postInfo(ctx, []byte(`{"type":"meta"}`), &meta); err != nil {
		return err
	}
	b.assets = make(map[string]asset, len(meta.Universe))
	for i, u := range meta.Universe {
		b.assets[u.Name] = asset{index: i, szDecimals: u.SzDecimals}
	}
	return nil
}

func (b *Broker) Sync(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"type": "clearinghouseState", "user": b.account.Hex()})
	var st struct {
		MarginSummary struct {
			AccountValue decimal.Decimal `json:"accountValue"`
		} `json:"marginSummary"`
		Withdrawable   decimal.Decimal `json:"withdrawable"`
		AssetPositions []struct {
			Position struct {
				Coin    string          `json:"coin"`
				Szi     decimal.Decimal `json:"szi"`
				EntryPx decimal.Decimal `json:"entryPx"`
			} `json:"position"`
		} `json:"assetPositions"`
	}
	if err := b.c.postInfo(ctx, body, &st); err != nil {
		return err
	}
	positions := make(map[string]position, len(st.AssetPositions))
	for _, ap := range st.AssetPositions {
		positions[ap.Position.Coin] = position{size: ap.Position.Szi, entryPx: ap.Position.EntryPx}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.equity = st.MarginSummary.AccountValue
	b.cash = st.Withdrawable
	b.positions = positions
	return nil
}

func (b *Broker) Position(symbol string) decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.positions[symbol].size
}

// Cash 返回可用保证金（withdrawable）。风控用它限制下单金额，相当于不加杠杆。
func (b *Broker) Cash() decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cash
}

// Equity 返回账户总价值（含未实现盈亏），由交易所计算，marks 参数不使用。
func (b *Broker) Equity(map[string]decimal.Decimal) decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.equity
}

// 下单 action 的结构。字段顺序和 msgpack tag 必须和官方 SDK 完全一致，否则签名会对不上。
type orderAction struct {
	Type     string      `msgpack:"type" json:"type"`
	Orders   []orderWire `msgpack:"orders" json:"orders"`
	Grouping string      `msgpack:"grouping" json:"grouping"`
}

type orderWire struct {
	Asset      int       `msgpack:"a" json:"a"`
	IsBuy      bool      `msgpack:"b" json:"b"`
	Price      string    `msgpack:"p" json:"p"`
	Size       string    `msgpack:"s" json:"s"`
	ReduceOnly bool      `msgpack:"r" json:"r"`
	OrderType  orderType `msgpack:"t" json:"t"`
}

type orderType struct {
	Limit limitOrder `msgpack:"limit" json:"limit"`
}

type limitOrder struct {
	Tif string `msgpack:"tif" json:"tif"`
}

// MarketOrder 用"立即成交否则取消"（IOC）的限价单模拟市价单：
// 买单价格 = 参考价 × (1 + 滑点)，卖单价格 = 参考价 × (1 - 滑点)，
// 这样即使行情剧烈波动，成交价也不会差过这个范围。
func (b *Broker) MarketOrder(ctx context.Context, symbol string, side model.Side, qty, refPrice decimal.Decimal) (model.Fill, error) {
	a, ok := b.assets[symbol]
	if !ok {
		return model.Fill{}, fmt.Errorf("hyperliquid 没有这个永续合约: %q", symbol)
	}
	size := qty.Truncate(a.szDecimals)
	if !size.IsPositive() {
		return model.Fill{}, fmt.Errorf("下单数量 %s 按精度（%d 位小数）截断后为 0", qty, a.szDecimals)
	}

	one := decimal.NewFromInt(1)
	isBuy := side == model.Buy
	px := refPrice.Mul(one.Sub(b.slippage))
	if isBuy {
		px = refPrice.Mul(one.Add(b.slippage))
	}
	px = roundPrice(px, a.szDecimals)

	filled, err := b.placeIOC(ctx, a, isBuy, px, size)
	if err != nil {
		return model.Fill{}, err
	}

	fill := model.Fill{
		Symbol: symbol, Side: side, Time: time.Now(),
		Qty: filled.TotalSz, Price: filled.AvgPx,
		Fee: filled.TotalSz.Mul(filled.AvgPx).Mul(b.feeRate),
	}
	if !isBuy {
		// 按开仓均价估算已实现盈亏（未计开仓手续费和资金费）
		b.mu.Lock()
		entry := b.positions[symbol].entryPx
		b.mu.Unlock()
		fill.RealizedPnL = fill.Price.Sub(entry).Mul(fill.Qty).Sub(fill.Fee)
	}
	return fill, nil
}

type iocFill struct {
	TotalSz decimal.Decimal `json:"totalSz"`
	AvgPx   decimal.Decimal `json:"avgPx"`
	Oid     int64           `json:"oid"`
}

// errNoMatch 表示 IOC 订单签名和授权都没问题，只是没有对手盘可以立即成交。
var errNoMatch = errors.New("没有可以立即成交的对手盘")

// placeIOC 提交一张 IOC 限价单。卖单一律 reduceOnly（只平仓，不开空）。
func (b *Broker) placeIOC(ctx context.Context, a asset, isBuy bool, px, size decimal.Decimal) (iocFill, error) {
	action := orderAction{
		Type: "order",
		Orders: []orderWire{{
			Asset: a.index, IsBuy: isBuy,
			Price: px.String(), Size: size.String(),
			ReduceOnly: !isBuy,
			OrderType:  orderType{Limit: limitOrder{Tif: "Ioc"}},
		}},
		Grouping: "na",
	}

	var resp struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	if err := b.postAction(ctx, action, &resp); err != nil {
		return iocFill{}, err
	}
	if resp.Status != "ok" {
		return iocFill{}, fmt.Errorf("hyperliquid 下单失败: %s", resp.Response)
	}
	var data struct {
		Data struct {
			Statuses []struct {
				Filled *iocFill `json:"filled"`
				Error  string   `json:"error"`
			} `json:"statuses"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Response, &data); err != nil {
		return iocFill{}, fmt.Errorf("hyperliquid 下单响应解析失败: %w (%s)", err, resp.Response)
	}
	if len(data.Data.Statuses) == 0 {
		return iocFill{}, fmt.Errorf("hyperliquid 下单响应为空: %s", resp.Response)
	}
	st := data.Data.Statuses[0]
	if st.Filled != nil {
		return *st.Filled, nil
	}
	if strings.Contains(strings.ToLower(st.Error), "immediately match") {
		return iocFill{}, fmt.Errorf("hyperliquid 订单未成交: %w", errNoMatch)
	}
	return iocFill{}, fmt.Errorf("hyperliquid 订单未成交: %s", st.Error)
}

// CheckAuth 验证签名和交易授权，不会产生成交：
// 提交一张价格只有参考价一半的 IOC 买单，交易所回复"无法立即成交"即说明一切正常。
// 只允许在测试网使用。
func (b *Broker) CheckAuth(ctx context.Context, symbol string, refPrice decimal.Decimal) error {
	if !b.c.testnet {
		return errors.New("CheckAuth 只能在测试网使用")
	}
	a, ok := b.assets[symbol]
	if !ok {
		return fmt.Errorf("hyperliquid 没有这个永续合约: %q", symbol)
	}
	px := roundPrice(refPrice.Div(decimal.NewFromInt(2)), a.szDecimals)
	// 名义金额要满足 10 美元的最小下单额，这里取 12 美元
	size := decimal.NewFromInt(12).Div(px).RoundUp(a.szDecimals)
	_, err := b.placeIOC(ctx, a, true, px, size)
	switch {
	case errors.Is(err, errNoMatch):
		return nil
	case err == nil:
		return errors.New("检查单意外成交了，请到测试网检查持仓")
	default:
		return err
	}
}

// postAction 签名并提交一个交易 action 到 /exchange。
func (b *Broker) postAction(ctx context.Context, action any, dst any) error {
	nonce := b.nextNonce()
	sig, err := signL1Action(b.key, action, nonce, b.c.testnet)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"action":       action,
		"nonce":        nonce,
		"signature":    sig,
		"vaultAddress": nil,
	})
	if err != nil {
		return err
	}
	return b.c.post(ctx, "/exchange", body, dst)
}

// nextNonce 返回毫秒时间戳作为 nonce，并保证严格递增（交易所会拒绝重复的 nonce）。
func (b *Broker) nextNonce() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := time.Now().UnixMilli()
	if n <= b.lastNonce {
		n = b.lastNonce + 1
	}
	b.lastNonce = n
	return n
}

// roundPrice 按 Hyperliquid 永续合约的价格规则取整：
// 最多 5 位有效数字，且小数位不超过 6 - szDecimals。整数价格总是允许的。
func roundPrice(px decimal.Decimal, szDecimals int32) decimal.Decimal {
	if !px.IsPositive() {
		return px
	}
	// 数量级：px = x × 10^exp，1 <= x < 10
	exp := int32(len(px.Truncate(0).String())) - 1
	if px.LessThan(decimal.NewFromInt(1)) {
		exp = -1
		for x := px.Shift(1); x.LessThan(decimal.NewFromInt(1)); x = x.Shift(1) {
			exp--
		}
	}
	decimals := min(max(4-exp, 0), 6-szDecimals)
	return px.Round(decimals)
}
