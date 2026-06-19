// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// TestHTTPRecorder verifies the recorder drives the package's HTTP metric vecs:
// the in-flight gauge balances across inc/dec and the counter increments under
// the {path, method, code} label set. (The request-level wiring — websocket
// bypass, status capture, per-route labelling — is exercised in
// pkg/utils/httpmux, which owns it.)
func TestHTTPRecorder(t *testing.T) {
	const path, method, code = "/metrics-recorder-test", "POST", "201"
	rec := HTTPRecorder{}

	before := testutil.ToFloat64(httpRequestsTotal.WithLabelValues(path, method, code))

	rec.InFlightInc(path, method)
	assert.Equal(t, float64(1), testutil.ToFloat64(httpRequestInFlight.WithLabelValues(path, method)),
		"in-flight gauge incremented")

	rec.Observe(path, method, 201, 5*time.Millisecond)
	rec.InFlightDec(path, method)

	assert.Zero(t, testutil.ToFloat64(httpRequestInFlight.WithLabelValues(path, method)),
		"in-flight gauge balanced after dec")
	assert.Equal(t, before+1, testutil.ToFloat64(httpRequestsTotal.WithLabelValues(path, method, code)),
		"request counter incremented under {path, method, code}")
}

// BenchmarkHTTPRecorder measures the per-request metrics cost (inc + observe +
// dec) an instrumented public-listener request pays. WithLabelValues avoids the
// per-call prometheus.Labels map the prior With(Labels{...}) form allocated.
func BenchmarkHTTPRecorder(b *testing.B) {
	var rec HTTPRecorder
	b.ReportAllocs()
	for b.Loop() {
		rec.InFlightInc("/bench", "GET")
		rec.Observe("/bench", "GET", 200, time.Millisecond)
		rec.InFlightDec("/bench", "GET")
	}
}
