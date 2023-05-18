package accesslog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fission/fission/pkg/utils/loggerfactory"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

func TestLogger(t *testing.T) {
	ctx := context.Background()
	logger := loggerfactory.GetLogger()
	shutdown, err := otelUtils.InitProvider(ctx, logger, "fission-router-test")
	assert.NoError(t, err)
	if shutdown != nil {
		defer shutdown(ctx)
	}

	r := mux.NewRouter()
	r.Use(Logger(logger))
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fission-router-test"))
	})
	handler := otelUtils.GetHandlerWithOTEL(r, "fission-router-test")
	s := httptest.NewServer(handler)
	defer s.Close()

	resp, err := http.Get(s.URL)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
