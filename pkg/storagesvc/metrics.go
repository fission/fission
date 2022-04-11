package storagesvc

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	functionLabels = []string{}
	totalArchives  = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_archives_total",
			Help: "Number of archives stored",
		},
		functionLabels,
	)
	totalMemoryUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_archive_memory_total",
			Help: "Amount of memory consumed by archives",
		},
		functionLabels,
	)
	archiveUploadingError = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_archive_upload_errors_total",
			Help: "Number of archives failed uploading",
		},
		functionLabels,
	)
)

func increaseArchives() {
	totalArchives.WithLabelValues().Inc()
}

func increaseMemory(memory float64) {
	totalMemoryUsage.WithLabelValues().Add(memory)
}

func increaseArchiveErrors() {
	archiveUploadingError.WithLabelValues().Inc()
}
