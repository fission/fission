package accesslog

import (
	"context"
	"net/http"
	"time"
)

type upstreamKey string

const upstreamCtxKey upstreamKey = "upstream"

type Upstream struct {
	Addr           string
	ResponseLength int64
	ResponseTime   int64
	ResponseStatus int
}

func emptyUpstream() *Upstream {
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
		u = emptyUpstream()
	}
	return u
}

func WithUpstream(ctx context.Context, u *Upstream) context.Context {
	return context.WithValue(ctx, upstreamCtxKey, u)
}

type UpstreamRoundTrip struct {
	http.RoundTripper
}

func NewUpstreamRoundTrip(roundTripper http.RoundTripper) *UpstreamRoundTrip {
	return &UpstreamRoundTrip{RoundTripper: roundTripper}
}

func (u *UpstreamRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	startAt := time.Now()
	uInfo := &Upstream{Addr: req.URL.String()}
	resp, err := u.RoundTripper.RoundTrip(req.WithContext(WithUpstream(ctx, uInfo)))
	uInfo.ResponseLength = resp.ContentLength
	uInfo.ResponseStatus = resp.StatusCode
	uInfo.ResponseTime = time.Since(startAt).Milliseconds()
	return resp, err
}
