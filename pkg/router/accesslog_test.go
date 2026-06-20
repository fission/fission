// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/correlation"
)

// capturingSink records Info log calls and their key/values so a test can
// assert the structured access record.
type capturingSink struct {
	mu   sync.Mutex
	logs []map[string]any
	msgs []string
}

func (s *capturingSink) Init(logr.RuntimeInfo) {}
func (s *capturingSink) Enabled(int) bool      { return true }
func (s *capturingSink) Info(_ int, msg string, kv ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	s.msgs = append(s.msgs, msg)
	s.logs = append(s.logs, m)
}
func (s *capturingSink) Error(error, string, ...any)    {}
func (s *capturingSink) WithValues(...any) logr.LogSink { return s }
func (s *capturingSink) WithName(string) logr.LogSink   { return s }

func (s *capturingSink) find(msg string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.msgs {
		if m == msg {
			return s.logs[i], true
		}
	}
	return nil, false
}

func TestAccessRecord(t *testing.T) {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "uid-1"}}
	backend, _ := url.Parse("http://10.0.0.5:8888")
	resp := &http.Response{StatusCode: 200}

	emit := func(accessLog bool) *capturingSink {
		sink := &capturingSink{}
		fh := functionHandler{logger: logr.New(sink), function: fn, accessLog: accessLog}
		req := httptest.NewRequest(http.MethodGet, "http://x/fn", nil)
		req = req.WithContext(correlation.NewContext(req.Context(), "req-xyz"))
		fh.collectFunctionMetric(time.Now(), &RetryingRoundTripper{serviceURL: backend, totalRetry: 1}, req, resp)
		return sink
	}

	t.Run("emits the access record when enabled", func(t *testing.T) {
		rec, ok := emit(true).find("function access")
		require.True(t, ok, "access record must be emitted when DISPLAY_ACCESS_LOG is on")
		assert.Equal(t, "req-xyz", rec["fission.request.id"])
		assert.Equal(t, "fn", rec["fission.function.name"])
		assert.Equal(t, "default", rec["fission.function.namespace"])
		assert.Equal(t, string(fn.UID), rec["fission.function.uid"])
		assert.Equal(t, 200, rec["http.status_code"])
		assert.Equal(t, "10.0.0.5:8888", rec["backend"])
	})

	t.Run("no access record when disabled (default)", func(t *testing.T) {
		_, ok := emit(false).find("function access")
		assert.False(t, ok, "access record must be off by default")
	})
}
