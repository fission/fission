package storagesvc

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	functionLabels = []string{}
	totalArchives  = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_archives_total",
			Help: "Number of archives stored",
		},
		functionLabels,
	)
	totalMemoryUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_archive_memory_bytes",
			Help: "Amount of memory consumed by archives",
		},
		functionLabels,
	)
)
