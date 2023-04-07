/*
Copyright 2022 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
	httpRequestDuration = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "http_requests_duration_seconds",
			Help:       "Time taken to serve the request by path and method.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
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
