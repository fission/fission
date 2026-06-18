// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/router/util"
)

type ResponseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of requests by path, method and status code.",
		},
		[]string{"path", "method", "code"},
	)
	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_requests_duration_seconds",
			Help: "Time taken to serve the request by path and method.",
			// Histogram instead of Summary: a summary allocates a per-series
			// quantile stream (the single largest router heap consumer), whereas
			// histogram buckets are fixed-size and aggregatable across replicas.
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path", "method"},
	)
	httpRequestInFlight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of requests currently being served by path and method.",
		},
		[]string{"path", "method"},
	)
)

func init() {
	Registry.MustRegister(httpRequestsTotal)
	Registry.MustRegister(httpRequestDuration)
	Registry.MustRegister(httpRequestInFlight)
}

func (rw *ResponseWriterWrapper) WriteHeader(statuscode int) {
	rw.statusCode = statuscode
	rw.ResponseWriter.WriteHeader(statuscode)
}

func (rw *ResponseWriterWrapper) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// InstrumentHandler wraps next, recording request metrics under a fixed,
// low-cardinality path label. Use it for static routes (e.g. stdlib ServeMux
// handlers) where the registered pattern is known at wiring time.
func InstrumentHandler(path string, next http.Handler) http.Handler {
	return instrument(func(*http.Request) string { return path }, next)
}

// InstrumentHandlerFunc wraps next, deriving the path label per request via
// pathFn. Use it where the matched route template is only known at request
// time — e.g. the router's gorilla mux, which supplies the template from
// mux.CurrentRoute. Keeping the mux-specific lookup in the caller is what lets
// this package stay router-agnostic.
func InstrumentHandlerFunc(pathFn func(*http.Request) string, next http.Handler) http.Handler {
	return instrument(pathFn, next)
}

// instrument is the shared metrics core. pathFn is evaluated BEFORE serving so
// the in-flight gauge inc/dec and the duration timer all key on the same path
// series; websocket upgrades bypass instrumentation (they never return until
// the socket closes).
func instrument(pathFn func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if util.IsWebsocketRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		labels := prometheus.Labels{
			"path":   pathFn(r),
			"method": r.Method,
		}
		rw := ResponseWriterWrapper{w, http.StatusOK}
		httpRequestInFlight.With(labels).Inc()
		timer := prometheus.NewTimer(httpRequestDuration.With(labels))
		defer func() {
			timer.ObserveDuration()
			httpRequestInFlight.With(labels).Dec()
			labels["code"] = strconv.Itoa(rw.statusCode)
			httpRequestsTotal.With(labels).Inc()
		}()
		next.ServeHTTP(&rw, r)
	})
}
