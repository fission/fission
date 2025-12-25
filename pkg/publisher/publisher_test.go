package publisher

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/utils/loggerfactory"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

func TestPublisher(t *testing.T) {
	fnName := "test-fn"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+fnName, r.URL.Path)
		require.Equal(t, "aaa", r.Header.Get("X-Fission-Test"))
		require.Contains(t, r.Header, "Traceparent")
	}))

	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	shutdown, err := otelUtils.InitProvider(ctx, logger, fnName)
	require.NoError(t, err)
	if shutdown != nil {
		defer shutdown(ctx)
	}

	wp := MakeWebhookPublisher(logger, s.URL)
	wp.Publish(ctx, "", map[string]string{"X-Fission-Test": "aaa"}, http.MethodPost, fnName)
	time.Sleep(time.Second * 1)
}

func TestPublisherSubpath(t *testing.T) {
	subpath := "/api/v1/read"
	fnName := "test-fn-subpath"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+fnName+subpath, r.URL.Path)
		require.Equal(t, "aaa", r.Header.Get("X-Fission-Test"))
		require.Contains(t, r.Header, "Traceparent")
	}))

	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	shutdown, err := otelUtils.InitProvider(ctx, logger, fnName)
	require.NoError(t, err)
	if shutdown != nil {
		defer shutdown(ctx)
	}

	wp := MakeWebhookPublisher(logger, s.URL)
	wp.Publish(ctx, "", map[string]string{"X-Fission-Test": "aaa"}, http.MethodGet, fnName+subpath)
	time.Sleep(time.Second * 1)
}
