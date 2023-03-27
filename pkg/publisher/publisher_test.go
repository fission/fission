package publisher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fission/fission/pkg/utils/loggerfactory"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
	"github.com/stretchr/testify/assert"
)

func TestPublisher(t *testing.T) {
	fnName := "test-fn"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/"+fnName, r.URL.Path)
		assert.Equal(t, "aaa", r.Header.Get("X-Fission-Test"))
		assert.Contains(t, r.Header, "Traceparent")
	}))

	ctx := context.Background()
	logger := loggerfactory.GetLogger()
	otelUtils.InitProvider(ctx, logger, fnName)

	wp := MakeWebhookPublisher(logger, s.URL)
	wp.Publish(ctx, "", map[string]string{"X-Fission-Test": "aaa"}, fnName)
	time.Sleep(time.Second * 1)
}
