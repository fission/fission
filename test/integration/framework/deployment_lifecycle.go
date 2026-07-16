// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// RestartDeployment bumps the restartedAt annotation on a control-plane
// Deployment (kubectl rollout restart, in-process) and returns the new
// generation for WaitForDeploymentRollout. Serial-suite only: restarting
// shared control-plane pods cannot run alongside the parallel common suite.
func (f *Framework) RestartDeployment(t *testing.T, ctx context.Context, name string) int64 {
	t.Helper()
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		restartedAtAnnotation, strconv.FormatInt(time.Now().UnixNano(), 10),
	)
	dep, err := f.kubeClient.AppsV1().Deployments(f.FissionNamespace()).Patch(
		ctx, name, k8stypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	require.NoErrorf(t, err, "RestartDeployment: patch %s", name)
	return dep.Generation
}

// WaitForDeploymentRollout blocks until the named Deployment has observed at
// least the given generation and every replica is updated and available —
// the generic form of WaitForExecutorRollout.
func (f *Framework) WaitForDeploymentRollout(t *testing.T, ctx context.Context, name string, atLeast int64, timeout time.Duration) {
	t.Helper()
	ns := f.FissionNamespace()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		dep, err := f.kubeClient.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get Deployment %s", name) {
			return
		}
		want := int32(1)
		if dep.Spec.Replicas != nil {
			want = *dep.Spec.Replicas
		}
		st := dep.Status
		assert.GreaterOrEqualf(c, st.ObservedGeneration, atLeast, "%s rollout not yet observed", name)
		assert.Equalf(c, want, st.UpdatedReplicas, "%s updated replicas", name)
		assert.Equalf(c, want, st.AvailableReplicas, "%s available replicas", name)
		assert.Equalf(c, want, st.Replicas, "%s total replicas (old pod terminating?)", name)
		assert.Zerof(c, st.UnavailableReplicas, "%s unavailable replicas", name)
	}, timeout, 2*time.Second)
}
