package crd

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	k8smetrics "k8s.io/client-go/tools/metrics"
)

var (
	// client metrics
	requestLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rest_client_request_latency_seconds",
			Help:    "Request latency in seconds by verb and URL.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
		},
		[]string{"verb", "url"},
	)

	requestResult = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rest_client_requests_total",
			Help: "Number of requests by status code, method, and host.",
		},
		[]string{"code", "method", "host"},
	)

	rateLimiterLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rest_client_rate_limiter_latency_seconds",
			Help:    "Rate limiter request latency in seconds by verb and URL.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
		},
		[]string{"verb", "url"},
	)
)

type requestLatencyMetricAdapter struct {
	metric *prometheus.HistogramVec
}

func (l *requestLatencyMetricAdapter) Observe(ctx context.Context, verb string, u url.URL, latency time.Duration) {
	l.metric.WithLabelValues(verb, u.String()).Observe(latency.Seconds())
}

type requestResultMetricAdapter struct {
	metric *prometheus.CounterVec
}

func (r *requestResultMetricAdapter) Increment(ctx context.Context, code, method, host string) {
	r.metric.WithLabelValues(code, method, host).Inc()
}

func registerK8sClientMetrics() {
	fmt.Println("Registering k8s client metrics")
	opts := k8smetrics.RegisterOpts{
		RequestLatency:     k8smetrics.LatencyMetric(&requestLatencyMetricAdapter{metric: requestLatency}),
		RequestResult:      k8smetrics.ResultMetric(&requestResultMetricAdapter{metric: requestResult}),
		RateLimiterLatency: k8smetrics.LatencyMetric(&requestLatencyMetricAdapter{metric: rateLimiterLatency}),
	}
	k8smetrics.Register(opts)
}

func init() {
	registerK8sClientMetrics()
	registerK8sCacheMetrics()
}
