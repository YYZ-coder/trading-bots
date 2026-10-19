package okx

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"trading-bots/internal/model"
)

// Broker 是 OKX 现货的实盘 Broker，只交易一个品种（例如 BTC-USDT）。
//
// 注意：持仓 = 账户里基础币（例如 BTC）的全部可用余额。卖出信号会卖掉全部余额，
// 所以请给机器人单独开一个子账户，不要和手动交易的资金混在一起。
type Broker struct {
	c                 *Client
	instID            string
	baseCcy, quoteCcy string
	lotSz, minSz      decimal.Decimal
	tickSz            decimal.Decimal
	slippage          decimal.Decimal // IOC 限价单相对参考价的最大偏离，例如 0.01 = 1%

	mu                  sync.Mutex
	baseAvail, baseEq   decimal.Decimal
	quoteAvail, quoteEq decimal.Decimal
}

func NewBroker(ctx context.Context, c *Client, instID string, slippage decimal.Decimal) (*Broker, error) {
	q := url.Values{}
	q.Set("instType", "SPOT")
	q.Set("instId", instID)
	var insts []struct {
		BaseCcy  string `json:"baseCcy"`
		QuoteCcy string `json:"quoteCcy"`
		LotSz    string `json:"lotSz"`
		MinSz    string `json:"minSz"`
		TickSz   string `json:"tickSz"`
		State    string `json:"state"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v5/public/instruments", q, nil, false, &insts); err != nil {
		return nil, err
	}
	if len(insts) == 0 {
		return nil, fmt.Errorf("okx 没有这个现货品种: %q", instID)
	}
	in := insts[0]
	if in.State != "live" {
		return nil, fmt.Errorf("okx %s 当前不可交易（state=%s）", instID, in.State)
	}
	b := &Broker{
		c: c, instID: instID, baseCcy: in.BaseCcy, quoteCcy: in.QuoteCcy,
		lotSz: dec(in.LotSz), minSz: dec(in.MinSz), tickSz: dec(in.TickSz),
		slippage: slippage,
	}
	if !b.lotSz.IsPositive() || !b.tickSz.IsPositive() {
		return nil, fmt.Errorf("okx %s 精度信息异常: lotSz=%q tickSz=%q", instID, in.LotSz, in.TickSz)
	}
	return b, b.Sync(ctx)
}

// dec 解析 OKX 返回的数字字符串。OKX 经常用空字符串表示 0。
func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func (b *Broker) Sync(ctx context.Context) error {
	q := url.Values{}
	q.Set("ccy", b.baseCcy+","+b.quoteCcy)
	var accts []struct {
		Details []struct {
			Ccy      string `json:"ccy"`
			AvailBal string `json:"availBal"`
			Eq       string `json:"eq"`
		} `json:"details"`
	}
	if err := b.c.do(ctx, http.MethodGet, "/api/v5/account/balance", q, nil, true, &accts); err != nil {
		return err
	}
	var baseAvail, baseEq, quoteAvail, quoteEq decimal.Decimal
	if len(accts) > 0 {
		for _, d := range accts[0].Details {
			switch d.Ccy {
			case b.baseCcy:
				baseAvail, baseEq = dec(d.AvailBal), dec(d.Eq)
			case b.quoteCcy:
				quoteAvail, quoteEq = dec(d.AvailBal), dec(d.Eq)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.baseAvail, b.baseEq, b.quoteAvail, b.quoteEq = baseAvail, baseEq, quoteAvail, quoteEq
	return nil
}

func (b *Broker) Position(symbol string) decimal.Decimal {
	if symbol != b.instID {
		return decimal.Zero
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.baseAvail
}

func (b *Broker) Cash() decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.quoteAvail
}

func (b *Broker) Equity(marks map[string]decimal.Decimal) decimal.Decimal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.quoteEq.Add(b.baseEq.Mul(marks[b.instID]))
}

// MarketOrder 用 IOC 限价单模拟市价单，价格偏离参考价不超过 slippage。
func (b *Broker) MarketOrder(ctx context.Context, symbol string, side model.Side, qty, refPrice decimal.Decimal) (model.Fill, error) {
	if symbol != b.instID {
		return model.Fill{}, fmt.Errorf("这个 Broker 只交易 %s，收到 %s", b.instID, symbol)
	}
	if side == model.Sell {
		qty = decimal.Min(qty, b.Position(symbol))
	}
	size := qty.Div(b.lotSz).Floor().Mul(b.lotSz)
	if size.LessThan(b.minSz) || !size.IsPositive() {
		return model.Fill{}, fmt.Errorf("下单数量 %s 低于最小下单量 %s", size, b.minSz)
	}
	one := decimal.NewFromInt(1)
	px := refPrice.Mul(one.Sub(b.slippage))
	okxSide := "sell"
	if side == model.Buy {
		px = refPrice.Mul(one.Add(b.slippage))
		okxSide = "buy"
	}
	px = px.Div(b.tickSz).Round(0).Mul(b.tickSz)

	clOrdID := "tb" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var placed []struct {
		OrdID string `json:"ordId"`
	}
	err := b.c.do(ctx, http.MethodPost, "/api/v5/trade/order", nil, map[string]string{
		"instId":  b.instID,
		"tdMode":  "cash", // 现货非杠杆
		"side":    okxSide,
		"ordType": "ioc",
		"px":      px.String(),
		"sz":      size.String(),
		"clOrdId": clOrdID,
	}, true, &placed)
	if err != nil {
		return model.Fill{}, err
	}
	if len(placed) == 0 {
		return model.Fill{}, fmt.Errorf("okx 下单响应为空（clOrdId=%s）", clOrdID)
	}
	return b.waitFill(ctx, side, placed[0].OrdID)
}

// waitFill 轮询订单直到进入终态，返回实际成交情况。
func (b *Broker) waitFill(ctx context.Context, side model.Side, ordID string) (model.Fill, error) {
	q := url.Values{}
	q.Set("instId", b.instID)
	q.Set("ordId", ordID)
	for range 20 {
		var orders []struct {
			State     string `json:"state"`
			AccFillSz string `json:"accFillSz"`
			AvgPx     string `json:"avgPx"`
			Fee       string `json:"fee"` // 负数表示扣费
			FeeCcy    string `json:"feeCcy"`
		}
		if err := b.c.do(ctx, http.MethodGet, "/api/v5/trade/order", q, nil, true, &orders); err != nil {
			return model.Fill{}, err
		}
		if len(orders) > 0 && (orders[0].State == "filled" || orders[0].State == "canceled" || orders[0].State == "mmp_canceled") {
			o := orders[0]
			filled := dec(o.AccFillSz)
			if !filled.IsPositive() {
				return model.Fill{}, fmt.Errorf("okx 订单 %s 未成交（价格超出滑点范围）", ordID)
			}
			avg := dec(o.AvgPx)
			fee := dec(o.Fee).Neg()
			if o.FeeCcy == b.baseCcy {
				fee = fee.Mul(avg) // 买入时手续费按基础币扣，换算成计价货币
			}
			return model.Fill{Symbol: b.instID, Side: side, Qty: filled, Price: avg, Fee: fee, Time: time.Now()}, nil
		}
		select {
		case <-ctx.Done():
			return model.Fill{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return model.Fill{}, fmt.Errorf("okx 订单 %s 5 秒内没有进入终态，请到交易所手动确认", ordID)
}
