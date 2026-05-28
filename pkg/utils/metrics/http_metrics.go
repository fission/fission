// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
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

func HTTPMetricMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if util.IsWebsocketRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		labels := make(prometheus.Labels, 0)
		labels["path"] = r.URL.Path
		if route := mux.CurrentRoute(r); route != nil {
			if routePath, err := route.GetPathTemplate(); err == nil {
				labels["path"] = routePath
			}
		}
		labels["method"] = r.Method
		rw := ResponseWriterWrapper{w, http.StatusOK}
		httpRequestInFlight.With(labels).Inc()
		httpRequestDuration := prometheus.NewTimer(httpRequestDuration.With(labels))
		defer func() {
			httpRequestDuration.ObserveDuration()
			httpRequestInFlight.With(labels).Dec()
			labels["code"] = fmt.Sprintf("%d", rw.statusCode)
			httpRequestsTotal.With(labels).Inc()
		}()
		next.ServeHTTP(&rw, r)
	})
}
