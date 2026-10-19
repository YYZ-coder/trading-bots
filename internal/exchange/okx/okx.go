// Package okx 实现 OKX 现货的行情和交易接口（API v5）。
//
// 模拟盘（Demo Trading）和实盘用同一个域名，区别是请求头 x-simulated-trading: 1，
// 以及模拟盘需要单独创建的 API Key。
package okx

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"

	"trading-bots/internal/exchange"
	"trading-bots/internal/model"
)

const (
	defaultBaseURL = "https://www.okx.com"
	liveWSURL      = "wss://ws.okx.com:8443/ws/v5/business" // K 线频道在 business 端点
	demoWSURL      = "wss://wspap.okx.com:8443/ws/v5/business"
	// history-candles 单次最多 100 根，限频 20 次/2 秒
	candlesPerRequest = 100
	requestInterval   = 120 * time.Millisecond
)

type Config struct {
	BaseURL    string // 默认 https://www.okx.com
	Demo       bool   // true = 模拟盘
	APIKey     string
	SecretKey  string
	Passphrase string
}

type Client struct {
	cfg  Config
	ws   string
	http *http.Client
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	ws := liveWSURL
	if cfg.Demo {
		ws = demoWSURL
	}
	return &Client{cfg: cfg, ws: ws, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) Name() string { return "okx" }

// okxBar 把通用周期（1m、1h、1d）转换成 OKX 的写法（1m、1H、1Dutc）。
// 日线及以上用 utc 版本，否则 OKX 默认按香港时间（UTC+8）切分 K 线。
func okxBar(interval string) (string, error) {
	if _, err := exchange.ParseInterval(interval); err != nil {
		return "", err
	}
	n, unit := interval[:len(interval)-1], interval[len(interval)-1]
	switch unit {
	case 'm':
		return n + "m", nil
	case 'h':
		if n == "6" || n == "12" {
			return n + "Hutc", nil
		}
		return n + "H", nil
	case 'd':
		return n + "Dutc", nil
	default: // 'w'
		return n + "Wutc", nil
	}
}

// response 是 OKX REST 接口的统一外层格式。
type response struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// do 发送请求。signed=true 时带上 API Key 签名。
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, signed bool, dst any) error {
	requestPath := path
	if len(query) > 0 {
		requestPath += "?" + query.Encode()
	}
	var bodyBytes []byte
	if body != nil {
		var err error
		if bodyBytes, err = json.Marshal(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+requestPath, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Demo {
		req.Header.Set("x-simulated-trading", "1")
	}
	if signed {
		if c.cfg.APIKey == "" || c.cfg.SecretKey == "" || c.cfg.Passphrase == "" {
			return errors.New("缺少 OKX API Key：请在 .env 里设置 OKX_API_KEY、OKX_SECRET_KEY、OKX_PASSPHRASE")
		}
		ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		req.Header.Set("OK-ACCESS-KEY", c.cfg.APIKey)
		req.Header.Set("OK-ACCESS-SIGN", sign(c.cfg.SecretKey, ts, method, requestPath, bodyBytes))
		req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
		req.Header.Set("OK-ACCESS-PASSPHRASE", c.cfg.Passphrase)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("okx %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("okx %s: %w", path, err)
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("okx %s: HTTP %d: %.300s", path, resp.StatusCode, raw)
	}
	if r.Code != "0" {
		// 下单类接口的具体错误在 data[0].sMsg 里
		var items []struct {
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		}
		if json.Unmarshal(r.Data, &items) == nil && len(items) > 0 && items[0].SMsg != "" {
			return fmt.Errorf("okx %s: code %s: %s (sCode %s: %s)", path, r.Code, r.Msg, items[0].SCode, items[0].SMsg)
		}
		return fmt.Errorf("okx %s: code %s: %s", path, r.Code, r.Msg)
	}
	if dst != nil {
		if err := json.Unmarshal(r.Data, dst); err != nil {
			return fmt.Errorf("okx %s: decode: %w", path, err)
		}
	}
	return nil
}

// sign 计算签名：Base64(HMAC-SHA256(secret, timestamp + method + requestPath + body))。
func sign(secret, ts, method, requestPath string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + method + requestPath))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// parseCandle 解析 [ts, o, h, l, c, vol, volCcy, volCcyQuote, confirm]。
// 返回的 bool 表示这根 K 线是否已收盘（confirm == "1"）。
func parseCandle(row []string, instID, interval string, dur time.Duration) (model.Candle, bool, error) {
	if len(row) < 9 {
		return model.Candle{}, false, fmt.Errorf("okx candle: unexpected row length %d", len(row))
	}
	ms, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil {
		return model.Candle{}, false, fmt.Errorf("okx candle: bad ts %q", row[0])
	}
	var vals [5]decimal.Decimal
	for i := range vals {
		if vals[i], err = decimal.NewFromString(row[i+1]); err != nil {
			return model.Candle{}, false, fmt.Errorf("okx candle: bad number %q", row[i+1])
		}
	}
	open := time.UnixMilli(ms)
	return model.Candle{
		Symbol: instID, Interval: interval,
		OpenTime: open, CloseTime: open.Add(dur - time.Millisecond),
		Open: vals[0], High: vals[1], Low: vals[2], Close: vals[3], Volume: vals[4],
	}, row[8] == "1", nil
}

// Candles 从 end 往前翻页拉取历史 K 线，直到覆盖 start。
func (c *Client) Candles(ctx context.Context, symbol, interval string, start, end time.Time) ([]model.Candle, error) {
	bar, err := okxBar(interval)
	if err != nil {
		return nil, err
	}
	dur, _ := exchange.ParseInterval(interval)

	var out []model.Candle
	after := end.UnixMilli()
	for {
		q := url.Values{}
		q.Set("instId", symbol)
		q.Set("bar", bar)
		q.Set("after", strconv.FormatInt(after, 10)) // 返回早于这个时间的数据
		q.Set("limit", strconv.Itoa(candlesPerRequest))
		var rows [][]string
		if err := c.do(ctx, http.MethodGet, "/api/v5/market/history-candles", q, nil, false, &rows); err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		// 返回结果按时间倒序（最新的在前）
		for _, row := range rows {
			k, closed, err := parseCandle(row, symbol, interval, dur)
			if err != nil {
				return nil, err
			}
			if closed && !k.OpenTime.Before(start) {
				out = append(out, k)
			}
			after = min(after, k.OpenTime.UnixMilli())
		}
		if after <= start.UnixMilli() || len(rows) < candlesPerRequest {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(requestInterval):
		}
	}
	slices.Reverse(out)
	return out, nil
}

func (c *Client) SubscribeCandles(ctx context.Context, symbol, interval string) (<-chan model.Candle, error) {
	bar, err := okxBar(interval)
	if err != nil {
		return nil, err
	}
	dur, _ := exchange.ParseInterval(interval)
	sub, _ := json.Marshal(map[string]any{
		"op":   "subscribe",
		"args": []map[string]string{{"channel": "candle" + bar, "instId": symbol}},
	})

	ch := make(chan model.Candle, 16)
	go func() {
		defer close(ch)
		exchange.KeepAlive(ctx, "okx", func(ctx context.Context) error {
			conn, _, err := websocket.Dial(ctx, c.ws, nil)
			if err != nil {
				return err
			}
			defer conn.CloseNow()
			conn.SetReadLimit(1 << 20)
			if err := conn.Write(ctx, websocket.MessageText, sub); err != nil {
				return err
			}

			// 30 秒内没有数据 OKX 会断开连接，所以每 20 秒发一次 "ping"
			connCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() {
				t := time.NewTicker(20 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-connCtx.Done():
						return
					case <-t.C:
						_ = conn.Write(connCtx, websocket.MessageText, []byte("ping"))
					}
				}
			}()

			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return err
				}
				if string(data) == "pong" {
					continue
				}
				var msg struct {
					Event string     `json:"event"`
					Msg   string     `json:"msg"`
					Data  [][]string `json:"data"`
				}
				if err := json.Unmarshal(data, &msg); err != nil {
					return fmt.Errorf("decode message: %w", err)
				}
				if msg.Event == "error" {
					return fmt.Errorf("订阅失败: %s", msg.Msg)
				}
				for _, row := range msg.Data {
					k, closed, err := parseCandle(row, symbol, interval, dur)
					if err != nil {
						return err
					}
					if !closed {
						continue
					}
					select {
					case ch <- k:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		})
	}()
	return ch, nil
}
