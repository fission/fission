package router

import (
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/fission/fission/pkg/tracker"
	"go.uber.org/zap"
)

func runAnalytics(logger *zap.Logger) {
	if os.Getenv(tracker.GA_TRACKING_ID) == "" {
		logger.Info("router analytics reporting disabled as GA_TRACKING_ID not set.")
		return
	}

	ticker := time.NewTicker(24 * time.Hour)
	for range ticker.C {
		err := tracker.Tracker.SendEvent(tracker.Event{
			Category: "router",
			Action:   "metrics",
			Label:    "functionCallCount",
			Value:    strconv.FormatUint(atomic.LoadUint64(&globalFunctionCallCount), 10),
		})
		if err != nil {
			logger.Error("failed to report analytics data", zap.Error(err))
		}
	}
}
