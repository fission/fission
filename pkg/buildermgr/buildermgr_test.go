// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWatcherRunnableNeedLeaderElection(t *testing.T) {
	w := &watcherRunnable{}
	assert.True(t, w.NeedLeaderElection(), "watchers must run on the leader only")
}

func TestWatcherRunnableReadyCheck(t *testing.T) {
	w := &watcherRunnable{}
	assert.Error(t, w.readyCheck(nil), "not ready until informers sync")

	w.ready.Store(true)
	assert.NoError(t, w.readyCheck(nil), "ready once informers sync")
}

func TestBindAddr(t *testing.T) {
	t.Setenv("METRICS_ADDR", "")
	assert.Equal(t, ":8080", bindAddr("METRICS_ADDR", "8080"))

	t.Setenv("METRICS_ADDR", "9090")
	assert.Equal(t, ":9090", bindAddr("METRICS_ADDR", "8080"))

	t.Setenv("METRICS_ADDR", "0.0.0.0:9090")
	assert.Equal(t, "0.0.0.0:9090", bindAddr("METRICS_ADDR", "8080"))
}
