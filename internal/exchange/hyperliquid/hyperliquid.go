// Package hyperliquid 实现 Hyperliquid（永续合约 DEX）的行情接口。
// 行情接口是公开的，不需要钱包或私钥。
package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"

	"trading-bots/internal/exchange"
	"trading-bots/internal/model"
)

const (
	mainnetAPI = "https://api.hyperliquid.xyz"
	testnetAPI = "https://api.hyperliquid-testnet.xyz"
)

type Client struct {
	apiURL  string
	wsURL   string
	testnet bool
	http    *http.Client
}

// New 创建客户端。testnet=true 时连接测试网。
func New(testnet bool) *Client {
	api := mainnetAPI
	if testnet {
		api = testnetAPI
	}
	return &Client{
		apiURL:  api,
		wsURL:   "wss://" + strings.TrimPrefix(api, "https://") + "/ws",
		testnet: testnet,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Name() string { return "hyperliquid" }

// hlCandle 是 REST 和 WebSocket 共用的 K 线格式。symbol 在 Hyperliquid 里叫 coin，例如 "BTC"。
type hlCandle struct {
	OpenTime  int64           `json:"t"`
	CloseTime int64           `json:"T"`
	Coin      string          `json:"s"`
	Interval  string          `json:"i"`
	Open      decimal.Decimal `json:"o"`
	Close     decimal.Decimal `json:"c"`
	High      decimal.Decimal `json:"h"`
	Low       decimal.Decimal `json:"l"`
	Volume    decimal.Decimal `json:"v"`
}

func (k hlCandle) toModel() model.Candle {
	return model.Candle{
		Symbol: k.Coin, Interval: k.Interval,
		OpenTime: time.UnixMilli(k.OpenTime), CloseTime: time.UnixMilli(k.CloseTime),
		Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume,
	}
}

// Candles 拉取历史 K 线。注意：Hyperliquid 每个周期只保留最近 5000 根。
func (c *Client) Candles(ctx context.Context, symbol, interval string, start, end time.Time) ([]model.Candle, error) {
	var out []model.Candle
	cursor := start
	for cursor.Before(end) {
		body, _ := json.Marshal(map[string]any{
			"type": "candleSnapshot",
			"req": map[string]any{
				"coin": symbol, "interval": interval,
				"startTime": cursor.UnixMilli(), "endTime": end.UnixMilli() - 1,
			},
		})
		var batch []hlCandle
		if err := c.postInfo(ctx, body, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, k := range batch {
			if time.UnixMilli(k.CloseTime).Before(time.Now()) {
				out = append(out, k.toModel())
			}
		}
		next := time.UnixMilli(batch[len(batch)-1].CloseTime + 1)
		if !next.After(cursor) {
			break
		}
		cursor = next
	}
	return out, nil
}

func (c *Client) postInfo(ctx context.Context, body []byte, dst any) error {
	return c.post(ctx, "/info", body, dst)
}

func (c *Client) post(ctx context.Context, path string, body []byte, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hyperliquid %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("hyperliquid %s: HTTP %d: %s", path, resp.StatusCode, msg)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("hyperliquid %s: decode: %w", path, err)
	}
	return nil
}

// SubscribeCandles 订阅实时 K 线。
// Hyperliquid 的推送没有"已收盘"标记，所以当收到新一根 K 线（开盘时间变了）时，
// 把上一根当作已收盘推出去。
func (c *Client) SubscribeCandles(ctx context.Context, symbol, interval string) (<-chan model.Candle, error) {
	ch := make(chan model.Candle, 16)
	sub, _ := json.Marshal(map[string]any{
		"method":       "subscribe",
		"subscription": map[string]any{"type": "candle", "coin": symbol, "interval": interval},
	})
	go func() {
		defer close(ch)
		exchange.KeepAlive(ctx, "hyperliquid", func(ctx context.Context) error {
			conn, _, err := websocket.Dial(ctx, c.wsURL, nil)
			if err != nil {
				return err
			}
			defer conn.CloseNow()
			conn.SetReadLimit(1 << 20)
			if err := conn.Write(ctx, websocket.MessageText, sub); err != nil {
				return err
			}

			// 服务器 60 秒收不到消息会断开连接，所以每 30 秒发一次心跳。
			connCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() {
				t := time.NewTicker(30 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-connCtx.Done():
						return
					case <-t.C:
						_ = conn.Write(connCtx, websocket.MessageText, []byte(`{"method":"ping"}`))
					}
				}
			}()

			var current *hlCandle
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return err
				}
				var msg struct {
					Channel string          `json:"channel"`
					Data    json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(data, &msg); err != nil {
					return fmt.Errorf("decode message: %w", err)
				}
				if msg.Channel != "candle" {
					continue // subscriptionResponse、pong 等
				}
				var k hlCandle
				if err := json.Unmarshal(msg.Data, &k); err != nil {
					return fmt.Errorf("decode candle: %w", err)
				}
				if current != nil && k.OpenTime > current.OpenTime {
					select {
					case ch <- current.toModel():
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				current = &k
			}
		})
	}()
	return ch, nil
}
