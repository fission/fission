package accesslog

import (
	"context"
)

type upstreamKey string

const upstreamCtxKey upstreamKey = "upstream"

type Upstream struct {
	Addr           string
	ResponseLength int
	ResponseTime   int
	ResponseStatus int
}

func NewUpstream() *Upstream {
	return &Upstream{
		Addr:           "-",
		ResponseLength: -1,
		ResponseTime:   -1,
		ResponseStatus: -1,
	}
}

func newUpstreamFrom(ctx context.Context) *Upstream {
	u, ok := ctx.Value(upstreamCtxKey).(*Upstream)
	if !ok {
		u = NewUpstream()
	}
	return u
}

func WithUpstream(ctx context.Context, u *Upstream) context.Context {
	return context.WithValue(ctx, upstreamCtxKey, u)
}
