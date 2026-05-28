// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"testing"
	"time"

	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestUpdateCPUUtilizationSvcStopsOnContextCancel guards against the goroutine
// leak where updateCPUUtilizationSvc looped on the ticker forever and ignored
// context cancellation, so it outlived the pool (one leaked goroutine per pool
// ever created). It must return promptly once its context is cancelled.
func TestUpdateCPUUtilizationSvcStopsOnContextCancel(t *testing.T) {
	gp := &GenericPool{
		logger:        loggerfactory.GetLogger(),
		metricsClient: metricsfake.NewSimpleClientset(),
		fnNamespace:   "fission-function",
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		gp.updateCPUUtilizationSvc(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("updateCPUUtilizationSvc did not return after its context was cancelled")
	}
}
