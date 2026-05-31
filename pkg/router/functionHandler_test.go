// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestProxyErrorHandler(t *testing.T) {
	logger := loggerfactory.GetLogger()

	fh := &functionHandler{
		logger: logger,
		function: &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dummy",
				Namespace: "dummy-bar",
			},
		},
	}

	errHandler := fh.getProxyErrorHandler(time.Now(), &RetryingRoundTripper{})

	req, err := http.NewRequest("GET", "http://foobar.com", nil)
	require.Nil(t, err)

	req.Header.Set("foo", "bar")
	respRecorder := httptest.NewRecorder()
	errHandler(respRecorder, req, context.Canceled)
	require.Equal(t, 499, respRecorder.Code)

	respRecorder = httptest.NewRecorder()
	errHandler(respRecorder, req, context.DeadlineExceeded)
	require.Equal(t, http.StatusGatewayTimeout, respRecorder.Code)

	respRecorder = httptest.NewRecorder()
	errHandler(respRecorder, req, errors.New("dummy"))
	require.Equal(t, http.StatusInternalServerError, respRecorder.Code)
}

// recordingExecutor implements eclient.ClientInterface and records the Function
// passed to GetServiceForFunction.
type recordingExecutor struct{ gotFn *fv1.Function }

func (r *recordingExecutor) GetServiceForFunction(_ context.Context, fn *fv1.Function) (string, error) {
	r.gotFn = fn
	return "10.0.0.1:8888", nil
}
func (r *recordingExecutor) TapService(metav1.ObjectMeta, fv1.ExecutorType, url.URL) {}
func (r *recordingExecutor) UnTapService(context.Context, metav1.ObjectMeta, fv1.ExecutorType, *url.URL) error {
	return nil
}

func fnWithPkg(rv, pkg string) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", ResourceVersion: rv}}
	fn.Spec.Package.PackageRef.Name = pkg
	return fn
}

// TestGetServiceEntryFromExecutorReReadsCurrentFunction guards the TestGoEnv fix:
// the executor must be specialized with the current Function (re-read from the
// Manager cache), not the resolver's stale snapshot — otherwise a poolmgr function
// keeps serving the old package after `fn update --pkg`.
func TestGetServiceEntryFromExecutorReReadsCurrentFunction(t *testing.T) {
	logger := loggerfactory.GetLogger()
	stale := fnWithPkg("1", "pkg-v1") // the resolver snapshot the handler captured
	fresh := fnWithPkg("2", "pkg-v2") // what the Manager cache now holds

	reader := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(fresh).Build()
	exec := &recordingExecutor{}
	fh := functionHandler{logger: logger, function: stale, reader: reader, executor: exec}

	_, err := fh.getServiceEntryFromExecutor(t.Context())
	require.NoError(t, err)
	require.NotNil(t, exec.gotFn)
	assert.Equal(t, "pkg-v2", exec.gotFn.Spec.Package.PackageRef.Name, "executor must get the re-read function, not the stale snapshot")
	assert.Equal(t, "2", exec.gotFn.ResourceVersion)
}

// TestGetServiceEntryFromExecutorFallsBackToSnapshot: with no reader the captured
// snapshot is used.
func TestGetServiceEntryFromExecutorFallsBackToSnapshot(t *testing.T) {
	logger := loggerfactory.GetLogger()
	snap := fnWithPkg("1", "pkg-v1")
	exec := &recordingExecutor{}
	fh := functionHandler{logger: logger, function: snap, executor: exec} // reader nil

	_, err := fh.getServiceEntryFromExecutor(t.Context())
	require.NoError(t, err)
	require.NotNil(t, exec.gotFn)
	assert.Equal(t, "pkg-v1", exec.gotFn.Spec.Package.PackageRef.Name)
}
