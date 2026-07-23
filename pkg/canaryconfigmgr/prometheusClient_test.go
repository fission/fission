// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spyPromAPI is a minimal prometheusv1.API fake that records every query
// string passed to Query, so tests can assert the FULL PromQL sent — not
// just that a function_version label is present, but that function_name and
// every other label is correct too (RFC-0025 plan-review blocker #2:
// passing the version name as function_name would match zero series and
// wedge the rollout in a permanent requeue). Embedding the interface (left
// nil) satisfies every method these tests don't call; only Query is
// implemented.
type spyPromAPI struct {
	prometheusv1.API
	queries []string
	value   model.Value
}

func (s *spyPromAPI) Query(_ context.Context, query string, _ time.Time, _ ...prometheusv1.Option) (model.Value, prometheusv1.Warnings, error) {
	s.queries = append(s.queries, query)
	return s.value, nil, nil
}

func newSpyClient() (*spyPromAPI, *PrometheusApiClient) {
	spy := &spyPromAPI{value: &model.Scalar{Value: 1}}
	return spy, &PrometheusApiClient{logger: logr.Discard(), client: spy}
}

func TestGetRequestsToFuncInWindow_QueryStrings(t *testing.T) {
	t.Run("function-pair mode: no function_version label", func(t *testing.T) {
		spy, c := newSpyClient()

		_, err := c.GetRequestsToFuncInWindow(t.Context(), "/orders", "GET", "new", "", "default", "1m")
		require.NoError(t, err)

		require.Len(t, spy.queries, 2)
		assert.Equal(t, `fission_function_calls_total{function_name="new",function_namespace="default",path="/orders",method="GET"}[1m]`, spy.queries[0])
		assert.Equal(t, `fission_function_calls_total{function_name="new",function_namespace="default",path="/orders",method="GET"} offset 1m`, spy.queries[1])
	})

	t.Run("alias mode: function_name is the FUNCTION, function_version is the version", func(t *testing.T) {
		spy, c := newSpyClient()

		_, err := c.GetRequestsToFuncInWindow(t.Context(), "/orders", "GET", "orders", "orders-v2", "default", "1m")
		require.NoError(t, err)

		require.Len(t, spy.queries, 2)
		assert.Equal(t, `fission_function_calls_total{function_name="orders",function_namespace="default",path="/orders",method="GET",function_version="orders-v2"}[1m]`, spy.queries[0])
		assert.Equal(t, `fission_function_calls_total{function_name="orders",function_namespace="default",path="/orders",method="GET",function_version="orders-v2"} offset 1m`, spy.queries[1])
	})
}

func TestGetTotalFailedRequestsToFuncInWindow_QueryStrings(t *testing.T) {
	t.Run("function-pair mode: no function_version label", func(t *testing.T) {
		spy, c := newSpyClient()

		_, err := c.GetTotalFailedRequestsToFuncInWindow(t.Context(), "new", "", "default", "/orders", "GET", "1m")
		require.NoError(t, err)

		require.Len(t, spy.queries, 2)
		assert.Equal(t, `fission_function_errors_total{function_name="new",function_namespace="default",path="/orders",method="GET"}[1m]`, spy.queries[0])
		assert.Equal(t, `fission_function_errors_total{function_name="new",function_namespace="default",path="/orders",method="GET"} offset 1m`, spy.queries[1])
	})

	t.Run("alias mode: function_name is the FUNCTION, function_version is the version", func(t *testing.T) {
		spy, c := newSpyClient()

		_, err := c.GetTotalFailedRequestsToFuncInWindow(t.Context(), "orders", "orders-v2", "default", "/orders", "GET", "1m")
		require.NoError(t, err)

		require.Len(t, spy.queries, 2)
		assert.Equal(t, `fission_function_errors_total{function_name="orders",function_namespace="default",path="/orders",method="GET",function_version="orders-v2"}[1m]`, spy.queries[0])
		assert.Equal(t, `fission_function_errors_total{function_name="orders",function_namespace="default",path="/orders",method="GET",function_version="orders-v2"} offset 1m`, spy.queries[1])
	})
}
