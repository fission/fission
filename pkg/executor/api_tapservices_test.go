// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/executor/executortype"
)

// tapStubExecutorType implements executortype.ExecutorType by embedding the
// interface (nil) and overriding only TapService, which is all tapServices
// touches.
type tapStubExecutorType struct {
	executortype.ExecutorType
	err error
}

func (s *tapStubExecutorType) TapService(_ context.Context, _ string) error {
	return s.err
}

// recordingSink captures whether Error() was ever invoked on the logger, so a
// test can assert routine churn does NOT log at error level.
type recordingSink struct {
	mu       sync.Mutex
	errCalls int
}

func (s *recordingSink) Init(logr.RuntimeInfo)          {}
func (s *recordingSink) Enabled(int) bool               { return true }
func (s *recordingSink) Info(int, string, ...any)       {}
func (s *recordingSink) WithValues(...any) logr.LogSink { return s }
func (s *recordingSink) WithName(string) logr.LogSink   { return s }
func (s *recordingSink) Error(error, string, ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errCalls++
}

func (s *recordingSink) errors() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errCalls
}

func tapBody(t *testing.T, reqs ...client.TapServiceRequest) *http.Request {
	t.Helper()
	b, err := json.Marshal(reqs)
	require.NoError(t, err)
	return httptest.NewRequest(http.MethodPost, "/v2/tapServices", bytes.NewReader(b))
}

func tapReq(et fv1.ExecutorType) client.TapServiceRequest {
	return client.TapServiceRequest{
		FnMetadata:     metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		FnExecutorType: et,
		ServiceURL:     "http://10.0.0.1:8888",
	}
}

func TestTapServices(t *testing.T) {
	notFound := ferror.MakeError(ferror.ErrorNotFound, "no such entry")

	tests := []struct {
		name          string
		stub          *tapStubExecutorType
		req           client.TapServiceRequest
		wantCode      int
		wantErrLevels int
	}{
		{
			name:          "success -> 200, no error log",
			stub:          &tapStubExecutorType{err: nil},
			req:           tapReq(fv1.ExecutorTypePoolmgr),
			wantCode:      http.StatusOK,
			wantErrLevels: 0,
		},
		{
			name:          "expired fsvc (NotFound) -> 404, no error log",
			stub:          &tapStubExecutorType{err: notFound},
			req:           tapReq(fv1.ExecutorTypePoolmgr),
			wantCode:      http.StatusNotFound,
			wantErrLevels: 0,
		},
		{
			name:          "wrapped NotFound -> 404, no error log",
			stub:          &tapStubExecutorType{err: fmt.Errorf("touch failed: %w", notFound)},
			req:           tapReq(fv1.ExecutorTypePoolmgr),
			wantCode:      http.StatusNotFound,
			wantErrLevels: 0,
		},
		{
			name:          "real error -> 404, error logged",
			stub:          &tapStubExecutorType{err: errors.New("kaboom")},
			req:           tapReq(fv1.ExecutorTypePoolmgr),
			wantCode:      http.StatusNotFound,
			wantErrLevels: 1,
		},
		{
			name:          "unknown executor type -> 404, error logged",
			stub:          &tapStubExecutorType{err: nil},
			req:           tapReq("does-not-exist"),
			wantCode:      http.StatusNotFound,
			wantErrLevels: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			e := &Executor{
				logger: logr.New(sink),
				executorTypes: map[fv1.ExecutorType]executortype.ExecutorType{
					fv1.ExecutorTypePoolmgr: tc.stub,
				},
			}
			rec := httptest.NewRecorder()
			e.tapServices(rec, tapBody(t, tc.req))
			assert.Equal(t, tc.wantCode, rec.Code)
			assert.Equal(t, tc.wantErrLevels, sink.errors(),
				"error-level log count mismatch")
		})
	}
}
