package context

import (
	"context"
	"time"
)

const (
	KeyDialTimeout  = "var.fission.router.dialtimeout"
	KeyAliveTimeout = "var.fission.router.alivetimeout"
	KeyMaxRetries   = "var.fission.router.maxretries"
)

func GetDialTimeout(ctx context.Context) time.Duration {
	if timeout, ok := ctx.Value(KeyDialTimeout).(time.Duration); ok {
		return timeout
	}
	return 0
}

func GetAliveTimeout(ctx context.Context) time.Duration {
	if timeout, ok := ctx.Value(KeyAliveTimeout).(time.Duration); ok {
		return timeout
	}
	return 0
}

func GetMaxRetries(ctx context.Context) int {
	if retries, ok := ctx.Value(KeyMaxRetries).(int); ok {
		return retries
	}
	return 0
}
