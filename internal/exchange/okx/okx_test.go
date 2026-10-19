package okx

import (
	"testing"
	"time"
)

func TestSign(t *testing.T) {
	// 期望值用 Python hmac/hashlib 独立计算
	got := sign("22582BD0CFF14C41EDBF1AB98506286D", "2020-12-08T09:08:57.715Z", "POST",
		"/api/v5/trade/order", []byte(`{"instId":"BTC-USDT","sz":"1"}`))
	if want := "TGG9gpeGW3L8JW/02jBsydBRt/UEJ3UzQ80H+KAhLEs="; got != want {
		t.Fatalf("sign = %s, want %s", got, want)
	}
}

func TestOKXBar(t *testing.T) {
	for in, want := range map[string]string{
		"1m": "1m", "15m": "15m", "1h": "1H", "4h": "4H", "6h": "6Hutc", "12h": "12Hutc", "1d": "1Dutc", "1w": "1Wutc",
	} {
		got, err := okxBar(in)
		if err != nil || got != want {
			t.Errorf("okxBar(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := okxBar("1x"); err == nil {
		t.Error("okxBar(1x) 应该报错")
	}
}

func TestParseCandle(t *testing.T) {
	row := []string{"1792411200000", "82357.6", "82689.1", "82316.6", "82607.5", "198.7", "0", "0", "1"}
	k, closed, err := parseCandle(row, "BTC-USDT", "1h", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !closed || k.Close.String() != "82607.5" || k.CloseTime.Sub(k.OpenTime) != time.Hour-time.Millisecond {
		t.Fatalf("unexpected candle: %+v closed=%v", k, closed)
	}
}
