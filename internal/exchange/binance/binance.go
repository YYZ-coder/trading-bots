// Package binance 实现币安现货的行情接口。
//
// 使用币安官方的公开行情镜像（data-api.binance.vision / data-stream.binance.vision），
// 不需要账号和 API Key。注意：这个镜像只提供行情，不能下单。
package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"

	"trading-bots/internal/exchange"
	"trading-bots/internal/model"
)

const (
	restURL = "https://data-api.binance.vision"
	wsURL   = "wss://data-stream.binance.vision/ws"
	// 币安 klines 接口单次最多返回 1000 根。
	maxKlinesPerRequest = 1000
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) Name() string { return "binance" }

func (c *Client) Candles(ctx context.Context, symbol, interval string, start, end time.Time) ([]model.Candle, error) {
	var out []model.Candle
	cursor := start
	for cursor.Before(end) {
		q := url.Values{}
		q.Set("symbol", strings.ToUpper(symbol))
		q.Set("interval", interval)
		q.Set("startTime", strconv.FormatInt(cursor.UnixMilli(), 10))
		q.Set("endTime", strconv.FormatInt(end.UnixMilli()-1, 10))
		q.Set("limit", strconv.Itoa(maxKlinesPerRequest))

		batch, err := c.fetchKlines(ctx, q)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, k := range batch {
			// 跳过还没收盘的最后一根
			if k.CloseTime.Before(time.Now()) {
				out = append(out, k)
			}
		}
		cursor = batch[len(batch)-1].CloseTime.Add(time.Millisecond)
		if len(batch) < maxKlinesPerRequest {
			break
		}
	}
	return out, nil
}

func (c *Client) fetchKlines(ctx context.Context, q url.Values) ([]model.Candle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, restURL+"/api/v3/klines?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance klines: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, fmt.Errorf("binance klines: HTTP %d: %s", resp.StatusCode, e.Msg)
	}

	// 每根 K 线是一个混合类型数组：[openTime, "open", "high", "low", "close", "volume", closeTime, ...]
	var raw [][]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("binance klines: decode: %w", err)
	}
	symbol, interval := q.Get("symbol"), q.Get("interval")
	out := make([]model.Candle, 0, len(raw))
	for _, r := range raw {
		if len(r) < 7 {
			return nil, fmt.Errorf("binance klines: unexpected row length %d", len(r))
		}
		var openMs, closeMs int64
		var o, h, l, cl, v decimal.Decimal
		for _, f := range []struct {
			dst any
			src json.RawMessage
		}{{&openMs, r[0]}, {&o, r[1]}, {&h, r[2]}, {&l, r[3]}, {&cl, r[4]}, {&v, r[5]}, {&closeMs, r[6]}} {
			if err := json.Unmarshal(f.src, f.dst); err != nil {
				return nil, fmt.Errorf("binance klines: parse field: %w", err)
			}
		}
		out = append(out, model.Candle{
			Symbol: symbol, Interval: interval,
			OpenTime: time.UnixMilli(openMs), CloseTime: time.UnixMilli(closeMs),
			Open: o, High: h, Low: l, Close: cl, Volume: v,
		})
	}
	return out, nil
}

// wsKline 对应 <symbol>@kline_<interval> 推送里的 "k" 字段。
type wsKline struct {
	OpenTime  int64           `json:"t"`
	CloseTime int64           `json:"T"`
	Symbol    string          `json:"s"`
	Interval  string          `json:"i"`
	Open      decimal.Decimal `json:"o"`
	Close     decimal.Decimal `json:"c"`
	High      decimal.Decimal `json:"h"`
	Low       decimal.Decimal `json:"l"`
	Volume    decimal.Decimal `json:"v"`
	Closed    bool            `json:"x"`
}

func (c *Client) SubscribeCandles(ctx context.Context, symbol, interval string) (<-chan model.Candle, error) {
	ch := make(chan model.Candle, 16)
	stream := fmt.Sprintf("%s/%s@kline_%s", wsURL, strings.ToLower(symbol), interval)
	go func() {
		defer close(ch)
		exchange.KeepAlive(ctx, "binance", func(ctx context.Context) error {
			conn, _, err := websocket.Dial(ctx, stream, nil)
			if err != nil {
				return err
			}
			defer conn.CloseNow()
			conn.SetReadLimit(1 << 20)
			for {
				// 币安每 3 分钟发一次 ping，coder/websocket 在 Read 时会自动回 pong。
				_, data, err := conn.Read(ctx)
				if err != nil {
					return err
				}
				var msg struct {
					K wsKline `json:"k"`
				}
				if err := json.Unmarshal(data, &msg); err != nil {
					return fmt.Errorf("decode kline: %w", err)
				}
				if !msg.K.Closed {
					continue
				}
				k := msg.K
				select {
				case ch <- model.Candle{
					Symbol: k.Symbol, Interval: k.Interval,
					OpenTime: time.UnixMilli(k.OpenTime), CloseTime: time.UnixMilli(k.CloseTime),
					Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume,
				}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
	}()
	return ch, nil
}
