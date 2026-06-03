// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestExecutorManagedSelector guards the Manager cache's Deployment/Service
// filter. An empty selector would mirror every Deployment/Service in the
// function namespace into the cache and OOM at scale — the regression that bit
// the per-type informer factories this selector replaces (issue #2775). It must
// match newdeploy- and container-managed objects and nothing else.
func TestExecutorManagedSelector(t *testing.T) {
	assert.False(t, executorManagedSelector.Empty(), "selector must filter by executor type, not match everything")

	assert.True(t, executorManagedSelector.Matches(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypeNewdeploy)}),
		"must match newdeploy-managed objects")
	assert.True(t, executorManagedSelector.Matches(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypeContainer)}),
		"must match container-managed objects")
	assert.False(t, executorManagedSelector.Matches(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)}),
		"poolmgr does not own per-function Deployments/Services read via this cache")
	assert.False(t, executorManagedSelector.Matches(labels.Set{}),
		"must not match an unlabelled object")
}
