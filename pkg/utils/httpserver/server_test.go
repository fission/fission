package httpserver

import (
	"context"
	"io/ioutil"
	"net/http"
	"testing"

	"github.com/gorilla/mux"
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
		URL        string
		StatusCode int
		Body       string
	}{
		{
			URL:        "http://localhost:8999",
			StatusCode: http.StatusOK,
			Body:       "test handler",
		},
		{
			URL:        "http://localhost:8999/notfound",
			StatusCode: http.StatusNotFound,
			Body:       "404 page not found\n",
		},
	}
	for _, test := range tests {
		resp, err := http.Get(test.URL)
		if err != nil {
			t.Errorf("failed to make get request %v: %v", test.URL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != test.StatusCode {
			t.Errorf("expected status code %v, got %v", test.StatusCode, resp.StatusCode)
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Errorf("failed to read response body: %v", err)
		}
		if string(body) != test.Body {
			t.Errorf("expected body \"%v\", got \"%v\"", test.Body, string(body))
		}
	}
}
