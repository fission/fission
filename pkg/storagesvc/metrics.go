package storagesvc

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	functionLabels = []string{}
	totalArchives  = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_archives",
			Help: "Number of archives stored",
		},
		functionLabels,
	)
	totalMemoryUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_archive_memory_bytes",
			Help: "Amount of memory consumed by archives",
		},
		functionLabels,
	)
)

func init() {
	registry := metrics.Registry
	registry.MustRegister(totalArchives)
	registry.MustRegister(totalMemoryUsage)
}
