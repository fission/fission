package crd

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	k8sCache "k8s.io/client-go/tools/cache"
)

var (
	// reflector metrics
	listsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "reflector",
		Name:      "lists_total",
		Help:      "Total number of API lists done by the reflectors.",
	}, []string{"name"})
	listsDuration = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: "reflector",
		Name:      "lists_duration_seconds",
		Help:      "API list latency in seconds by the reflectors.",
	}, []string{"name"})
	itemsPerList = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: "reflector",
		Name:      "items_per_list",
		Help:      "Number of items returned in API lists by the reflectors.",
	}, []string{"name"})
	watchesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "reflector",
		Name:      "watches_total",
		Help:      "Total number of API watches done by the reflectors.",
	}, []string{"name"})
	shortWatchesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "reflector",
		Name:      "short_watches_total",
		Help:      "Total number of short API watches done by the reflectors.",
	}, []string{"name"})
	watchDuration = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: "reflector",
		Name:      "watch_duration_seconds",
		Help:      "API watch latency in seconds by the reflectors.",
	}, []string{"name"})
	itemsPerWatch = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: "reflector",
		Name:      "items_per_watch",
		Help:      "Number of items returned in API watches by the reflectors.",
	}, []string{"name"})
	lastResourceVersion = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "reflector",
		Name:      "last_resource_version",
		Help:      "Last resource version seen by the reflectors.",
	}, []string{"name"})
)

type reflectorMetricsAdapter struct {
	listsTotal          *prometheus.CounterVec
	listsDuration       *prometheus.SummaryVec
	itemsPerList        *prometheus.SummaryVec
	watchesTotal        *prometheus.CounterVec
	shortWatchesTotal   *prometheus.CounterVec
	watchDuration       *prometheus.SummaryVec
	itemsPerWatch       *prometheus.SummaryVec
	lastResourceVersion *prometheus.GaugeVec
}

func (r *reflectorMetricsAdapter) NewListsMetric(name string) k8sCache.CounterMetric {
	return r.listsTotal.WithLabelValues(name)
}

func (r *reflectorMetricsAdapter) NewListDurationMetric(name string) k8sCache.SummaryMetric {
	return r.listsDuration.WithLabelValues(name)
}

func (r *reflectorMetricsAdapter) NewItemsInListMetric(name string) k8sCache.SummaryMetric {
	return r.itemsPerList.WithLabelValues(name)
}

func (r *reflectorMetricsAdapter) NewWatchesMetric(name string) k8sCache.CounterMetric {
	return r.watchesTotal.WithLabelValues(name)
}

func (r *reflectorMetricsAdapter) NewShortWatchesMetric(name string) k8sCache.CounterMetric {
	return r.shortWatchesTotal.WithLabelValues(name)
}

func (r *reflectorMetricsAdapter) NewWatchDurationMetric(name string) k8sCache.SummaryMetric {
	return r.watchDuration.WithLabelValues(name)
}

func (r *reflectorMetricsAdapter) NewItemsInWatchMetric(name string) k8sCache.SummaryMetric {
	return r.itemsPerWatch.WithLabelValues(name)
}

func (r *reflectorMetricsAdapter) NewLastResourceVersionMetric(name string) k8sCache.GaugeMetric {
	return r.lastResourceVersion.WithLabelValues(name)
}

func registerK8sCacheMetrics() {
	fmt.Println("Registering k8s cache metrics")
	k8sCache.SetReflectorMetricsProvider(&reflectorMetricsAdapter{
		listsTotal:          listsTotal,
		listsDuration:       listsDuration,
		itemsPerList:        itemsPerList,
		watchesTotal:        watchesTotal,
		shortWatchesTotal:   shortWatchesTotal,
		watchDuration:       watchDuration,
		itemsPerWatch:       itemsPerWatch,
		lastResourceVersion: lastResourceVersion,
	})
}
