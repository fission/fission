package buildermgr

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	functionLabels  = []string{"package_name, package_namespace"}
	packagesCreated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_packages_created_total",
			Help: "Number of packages created",
		},
		[]string{},
	)
	packageBuildError = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_packages_creation_error_total",
			Help: "Number of packages failed due to errors",
		},
		[]string{},
	)
	packageBuildDuration = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_package_creation_duration_seconds",
			Help:       "Time taken to build the package",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionLabels,
	)
)

func increasePackageCounter() {
	packagesCreated.WithLabelValues().Inc()
}

func increasePackageErrorCounter() {
	packageBuildError.WithLabelValues().Inc()
}

func observePackageCreationDuration(pkgname, pkgnamespace string, time float64) {
	packageBuildDuration.WithLabelValues(pkgname, pkgnamespace).Observe(time)
}
