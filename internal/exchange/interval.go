package exchange

import (
	"fmt"
	"strconv"
	"time"
)

// ParseInterval 把 "15m"、"4h"、"1d"、"1w" 这类 K 线周期转换成时长。
func ParseInterval(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("无效的 K 线周期 %q", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("无效的 K 线周期 %q", s)
	}
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("无效的 K 线周期 %q（支持 m/h/d/w）", s)
	}
	return time.Duration(n) * unit, nil
}
