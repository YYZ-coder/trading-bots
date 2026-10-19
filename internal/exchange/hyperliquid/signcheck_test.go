package hyperliquid

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

// TestTestnetSignature 用随机空钱包向测试网下单。交易所会用签名反推签名者地址并在报错里返回，
// 如果和我们的地址一致，说明签名算法正确。需要联网，设置 HL_SIGN_CHECK=1 才运行。
func TestTestnetSignature(t *testing.T) {
	if os.Getenv("HL_SIGN_CHECK") != "1" {
		t.Skip("设置 HL_SIGN_CHECK=1 运行联网签名检查")
	}
	ctx := context.Background()
	key, _ := crypto.GenerateKey()
	addr := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	b, err := NewBroker(ctx, New(true), hex.EncodeToString(crypto.FromECDSA(key)), "", decimal.RequireFromString("0.01"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	var mids map[string]decimal.Decimal
	if err := b.c.postInfo(ctx, []byte(`{"type":"allMids"}`), &mids); err != nil {
		t.Fatal(err)
	}
	// 一个下标小于 128 的币种，一个大于 128 的币种（检查 msgpack 整数编码）
	var high string
	for name, a := range b.assets {
		if a.index > 200 && !mids[name].IsZero() {
			high = name
			break
		}
	}
	for _, coin := range []string{"BTC", high} {
		qty := decimal.New(10, -b.assets[coin].szDecimals) // 10 个最小下单单位
		_, err := b.MarketOrder(ctx, coin, model.Buy, qty, mids[coin])
		t.Logf("%s (index %d): %v", coin, b.assets[coin].index, err)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), addr) {
			t.Errorf("%s: 报错里没有我们的地址 %s，签名可能不对", coin, addr)
		}
	}
}
