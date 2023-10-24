package httpserver

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestStartServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := loggerfactory.GetLogger()
	m := mux.NewRouter()
	m.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("test handler"))
		if err != nil {
			logger.Error("failed to write response", zap.Error(err))
		}
	}))
	go StartServer(ctx, logger, "test", "8999", m)

	tests := []struct {
		Name       string
		URL        string
		StatusCode int
		Body       string
	}{
		{
			Name:       "test handler",
			URL:        "http://localhost:8999",
			StatusCode: http.StatusOK,
			Body:       "test handler",
		},
		{
			Name:       "not found",
			URL:        "http://localhost:8999/notfound",
			StatusCode: http.StatusNotFound,
			Body:       "404 page not found\n",
		},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			resp, err := http.Get(test.URL)
			require.NoError(t, err, "failed to make get request %s", test.URL)
			defer resp.Body.Close()
			require.Equal(t, test.StatusCode, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, string(body), test.Body)
		})
	}
}
