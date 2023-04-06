package accesslog

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWithUpstream(t *testing.T) {
	u := &Upstream{
		Addr:           "http://127.0.0.1:8080/fission-function/fn-1",
		ResponseLength: 100,
		ResponseTime:   int64(time.Second),
		ResponseStatus: http.StatusOK,
	}
	ctx := WithUpstream(context.Background(), u)

	pu := newUpstreamFrom(ctx)
	assert.Equal(t, u, pu)
}
