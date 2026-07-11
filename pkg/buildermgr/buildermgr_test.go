// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadinessRunnableNeedLeaderElection(t *testing.T) {
	r := &readinessRunnable{}
	assert.True(t, r.NeedLeaderElection(), "readiness must reflect leadership (leader-only)")
}

func TestReadinessRunnableCheck(t *testing.T) {
	r := &readinessRunnable{}
	assert.Error(t, r.check(nil), "not ready until caches sync")

	r.ready.Store(true)
	assert.NoError(t, r.check(nil), "ready once caches sync")
}

func TestPackageBuildConcurrency(t *testing.T) {
	t.Setenv("BUILDERMGR_PACKAGE_CONCURRENCY", "")
	assert.Equal(t, defaultPackageBuildConcurrency, packageBuildConcurrency(), "unset falls back to default")

	t.Setenv("BUILDERMGR_PACKAGE_CONCURRENCY", "12")
	assert.Equal(t, 12, packageBuildConcurrency(), "valid value is honoured")

	t.Setenv("BUILDERMGR_PACKAGE_CONCURRENCY", "0")
	assert.Equal(t, defaultPackageBuildConcurrency, packageBuildConcurrency(), "non-positive falls back to default")

	t.Setenv("BUILDERMGR_PACKAGE_CONCURRENCY", "notanumber")
	assert.Equal(t, defaultPackageBuildConcurrency, packageBuildConcurrency(), "invalid falls back to default")
}
