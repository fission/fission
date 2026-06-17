// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestExecutorCacheOptionsTierSplit is the security guard for the executor's
// dynamic-tenancy cache. Tier A (Function/Environment) goes cluster-wide so a
// runtime-onboarded namespace is visible, but Tier B (Secret/ConfigMap) MUST
// stay namespace-scoped — a cluster-wide Secret cache would mirror every Secret
// in the cluster into the executor's memory, the single thing the design forbids
// (PRD §4.1). This test fails loudly if a refactor ever lets Secrets/ConfigMaps
// go cluster-wide.
func TestExecutorCacheOptionsTierSplit(t *testing.T) {
	perTypeOverrides := func(opts crcache.Options) (secret, cm, rs *crcache.ByObject) {
		for obj, bo := range opts.ByObject {
			bo := bo
			switch obj.(type) {
			case *corev1.Secret:
				secret = &bo
			case *corev1.ConfigMap:
				cm = &bo
			case *appsv1.ReplicaSet:
				rs = &bo
			}
		}
		return secret, cm, rs
	}

	t.Run("non-dynamic scopes every type to the env namespaces", func(t *testing.T) {
		t.Setenv("FISSION_DYNAMIC_NAMESPACES", "false")
		opts := executorCacheOptions()
		assert.NotEmpty(t, opts.DefaultNamespaces, "per-namespace cache by default")
		secret, cm, rs := perTypeOverrides(opts)
		assert.Nil(t, secret, "no per-type Secret override needed when the default is already namespace-scoped")
		assert.Nil(t, cm, "no per-type ConfigMap override needed when the default is already namespace-scoped")
		assert.Nil(t, rs, "no per-type ReplicaSet override needed when the default is already namespace-scoped")
	})

	t.Run("dynamic keeps Secrets and ConfigMaps namespace-scoped", func(t *testing.T) {
		t.Setenv("FISSION_DYNAMIC_NAMESPACES", "true")
		opts := executorCacheOptions()
		assert.Empty(t, opts.DefaultNamespaces, "Function/Environment cluster-wide in dynamic mode")
		secret, cm, rs := perTypeOverrides(opts)
		require.NotNil(t, secret, "Secret must carry a per-type namespace override in dynamic mode")
		require.NotNil(t, cm, "ConfigMap must carry a per-type namespace override in dynamic mode")
		require.NotNil(t, rs, "ReplicaSet must carry a per-type namespace override in dynamic mode")
		assert.NotEmpty(t, secret.Namespaces, "Secret cache must be namespace-scoped, NEVER cluster-wide")
		assert.NotEmpty(t, cm.Namespaces, "ConfigMap cache must be namespace-scoped, NEVER cluster-wide")
		assert.NotEmpty(t, rs.Namespaces, "ReplicaSet cache must be namespace-scoped to avoid mirroring the whole cluster")
	})
}

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
