// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/correlation"
)

// accessRecord is the structured access log line, decoded from the JSON the
// funcr sink emits, naming exactly the fields the test asserts.
type accessRecord struct {
	Msg               string `json:"msg"`
	RequestID         string `json:"fission.request.id"`
	FunctionName      string `json:"fission.function.name"`
	FunctionNamespace string `json:"fission.function.namespace"`
	FunctionUID       string `json:"fission.function.uid"`
	StatusCode        int    `json:"http.status_code"`
	Backend           string `json:"backend"`
}

// capturingSink records every Info line as JSON so a test can decode the one
// it cares about into accessRecord.
type capturingSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *capturingSink) logger() logr.Logger {
	return funcr.NewJSON(func(obj string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lines = append(s.lines, obj)
	}, funcr.Options{})
}

// find returns the first record whose msg matches.
func (s *capturingSink) find(t *testing.T, msg string) (accessRecord, bool) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.lines {
		var rec accessRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "log line must be JSON: %s", line)
		if rec.Msg == msg {
			return rec, true
		}
	}
	return accessRecord{}, false
}

func TestAccessRecord(t *testing.T) {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "uid-1"}}
	backend, _ := url.Parse("http://10.0.0.5:8888")
	resp := &http.Response{StatusCode: 200}

	emit := func(accessLog bool) *capturingSink {
		sink := &capturingSink{}
		fh := functionHandler{logger: sink.logger(), function: fn, accessLog: accessLog}
		req := httptest.NewRequest(http.MethodGet, "http://x/fn", nil)
		req = req.WithContext(correlation.NewContext(req.Context(), "req-xyz"))
		fh.collectFunctionMetric(time.Now(), &RetryingRoundTripper{serviceURL: backend, totalRetry: 1}, req, resp)
		return sink
	}

	t.Run("emits the access record when enabled", func(t *testing.T) {
		rec, ok := emit(true).find(t, "function access")
		require.True(t, ok, "access record must be emitted when DISPLAY_ACCESS_LOG is on")
		assert.Equal(t, "req-xyz", rec.RequestID)
		assert.Equal(t, "fn", rec.FunctionName)
		assert.Equal(t, "default", rec.FunctionNamespace)
		assert.Equal(t, string(fn.UID), rec.FunctionUID)
		assert.Equal(t, 200, rec.StatusCode)
		assert.Equal(t, "10.0.0.5:8888", rec.Backend)
	})

	t.Run("no access record when disabled (default)", func(t *testing.T) {
		_, ok := emit(false).find(t, "function access")
		assert.False(t, ok, "access record must be off by default")
	})
}
