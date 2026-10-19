package exchange

import (
	"context"
	"log/slog"
	"time"
)

// KeepAlive 反复运行 connect，直到 ctx 被取消。connect 返回（断线或出错）后
// 按指数退避（1s、2s、4s……最长 30s）重连。连接稳定超过 1 分钟后退避时间重置。
func KeepAlive(ctx context.Context, name string, connect func(ctx context.Context) error) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("websocket 断开，准备重连", "source", name, "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}
