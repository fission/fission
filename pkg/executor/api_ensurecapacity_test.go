// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/executor/dispatch"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
)

// capacityCaller is the facet of the executor client the router's fallback
// resolver type-asserts to (router.CapacityClient); declaring it here keeps
// the wire test honest about what the consumer actually calls.
type capacityCaller interface {
	EnsureCapacity(ctx context.Context, fn *fv1.Function, observedReady, observedBusy int) (string, error)
}

// capacityStubExecutorType drives ensureCapacityHandler end to end: it
// implements the optional capacityReserver facet plus the GetFuncSvc /
// cleanup methods the specialization path touches.
type capacityStubExecutorType struct {
	executortype.ExecutorType
	reserveErr error
	fsvc       *fscache.FuncSvc
	getErr     error

	reserves atomic.Int32
	failures atomic.Int32
}

func (s *capacityStubExecutorType) ReserveCapacity(_ context.Context, _ *metav1.ObjectMeta, _ int) error {
	s.reserves.Add(1)
	return s.reserveErr
}

func (s *capacityStubExecutorType) GetFuncSvc(_ context.Context, _ *fv1.Function) (*fscache.FuncSvc, error) {
	return s.fsvc, s.getErr
}

func (s *capacityStubExecutorType) UnTapService(_ context.Context, _ *metav1.ObjectMeta, _ string) {}

func (s *capacityStubExecutorType) MarkSpecializationFailure(_ context.Context, _ *metav1.ObjectMeta) {
	s.failures.Add(1)
}

func poolmgrFn(name string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "test-uid"},
		Spec: fv1.FunctionSpec{
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr},
			},
		},
	}
}

// TestEnsureCapacityWireContract locks the HTTP contract between the REAL
// executor client and the REAL handler chain (GetHandler, pass-through HMAC):
// the router-side fallback resolver is tested only against stubs that assume
// this contract, so a drift here (status codes, raw-address body shape) would
// otherwise surface only in production under load.
func TestEnsureCapacityWireContract(t *testing.T) {
	logger := logr.Discard()

	newExecutor := func(stub *capacityStubExecutorType) *Executor {
		return &Executor{
			logger: logger,
			executorTypes: map[fv1.ExecutorType]executortype.ExecutorType{
				fv1.ExecutorTypePoolmgr: stub,
			},
			dispatcher: dispatch.New[*fscache.FuncSvc](logger, 0),
		}
	}

	t.Run("success returns the raw pod address", func(t *testing.T) {
		stub := &capacityStubExecutorType{fsvc: &fscache.FuncSvc{
			Function: &metav1.ObjectMeta{Name: "fn", Namespace: "default"},
			Address:  "10.1.2.3:8888",
		}}
		srv := httptest.NewServer(newExecutor(stub).GetHandler())
		defer srv.Close()
		c, ok := client.MakeClient(logger, srv.URL, nil).(capacityCaller)
		require.True(t, ok, "executor client must implement the capacity facet")

		addr, err := c.EnsureCapacity(t.Context(), poolmgrFn("fn"), 2, 2)
		require.NoError(t, err)
		assert.Equal(t, "10.1.2.3:8888", addr, "the body is the raw address, not JSON")
		assert.EqualValues(t, 1, stub.reserves.Load(), "capacity must be reserved before specializing")
		assert.EqualValues(t, 0, stub.failures.Load())
	})

	// 429 is asserted at the raw HTTP layer: the retrying client would back
	// off for seconds on 429/5xx, and the ferror mapping for 429 is already
	// locked by pkg/error's own tests.
	t.Run("concurrency cap answers 429 on the wire", func(t *testing.T) {
		stub := &capacityStubExecutorType{
			reserveErr: ferror.MakeError(ferror.ErrorTooManyRequests, "at concurrency cap"),
		}
		srv := httptest.NewServer(newExecutor(stub).GetHandler())
		defer srv.Close()

		resp, err := http.Post(srv.URL+"/v2/ensureCapacity", "application/json", capacityBody(t, poolmgrFn("fn")))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode,
			"the router relays this 429 to the caller; a 500 here becomes a 5xx storm at saturation")
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "at concurrency cap")
	})

	t.Run("specialization failure releases the reservation", func(t *testing.T) {
		// A non-retryable code (400) keeps the retrying client to one attempt,
		// making the reserve/release balance exact.
		stub := &capacityStubExecutorType{
			getErr: ferror.MakeError(ferror.ErrorInvalidArgument, "specialization blew up"),
		}
		srv := httptest.NewServer(newExecutor(stub).GetHandler())
		defer srv.Close()
		c := client.MakeClient(logger, srv.URL, nil).(capacityCaller)

		_, err := c.EnsureCapacity(t.Context(), poolmgrFn("fn"), 1, 1)
		require.Error(t, err)
		assert.Equal(t, stub.reserves.Load(), stub.failures.Load(),
			"every reservation must be balanced by a MarkSpecializationFailure on error")
		assert.EqualValues(t, 1, stub.failures.Load())
	})

	t.Run("non-poolmgr function is a 400 without reserving", func(t *testing.T) {
		stub := &capacityStubExecutorType{}
		srv := httptest.NewServer(newExecutor(stub).GetHandler())
		defer srv.Close()
		c := client.MakeClient(logger, srv.URL, nil).(capacityCaller)

		fn := poolmgrFn("fn")
		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeNewdeploy
		_, err := c.EnsureCapacity(t.Context(), fn, 1, 1)
		require.Error(t, err)
		var fe ferror.Error
		require.ErrorAs(t, err, &fe)
		assert.EqualValues(t, ferror.ErrorInvalidArgument, fe.Code)
		assert.EqualValues(t, 0, stub.reserves.Load())
	})

	t.Run("malformed body is a 400", func(t *testing.T) {
		stub := &capacityStubExecutorType{}
		srv := httptest.NewServer(newExecutor(stub).GetHandler())
		defer srv.Close()

		resp, err := http.Post(srv.URL+"/v2/ensureCapacity", "application/json", bytes.NewBufferString("{not json"))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func capacityBody(t *testing.T, fn *fv1.Function) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(client.EnsureCapacityRequest{Function: fn, ObservedReadyEndpoints: 1, ObservedBusyEndpoints: 1})
	require.NoError(t, err)
	return bytes.NewBuffer(b)
}
