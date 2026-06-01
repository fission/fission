// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"

	"github.com/fission/fission/test/integration/framework"
)

// TestContainerUpdate exercises the container executor's updateFunction, which
// had no update coverage (backend_container_test only covers create+invoke).
// Both reachable executor-side update branches are driven via
// `fission fn update-container` and asserted on the live Kubernetes objects:
//
//   - scale change (--minscale/--maxscale) → the InvokeStrategy-changed branch
//     reconciles the HPA bounds.
//   - configmap-list change → the deployChanged branch calls updateFuncDeployment,
//     which rewrites the Deployment pod template (here: the EnvFrom sources).
//
// update-container replaces the whole configmap list (it does not merge), so the
// configmap step re-passes the original PORT configmap alongside the new one to
// keep the image listening on the NetworkPolicy-permitted port.
func TestContainerUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	image := f.Images().RequireContainer(t)

	const port = 8888

	ns := f.NewTestNamespace(t)
	fnName := "fn-ctrupd-" + ns.ID
	portCM := "ctrupd-port-" + ns.ID
	routePath := "/" + fnName

	// PORT (injected via this configmap) makes the user image listen on the
	// NetworkPolicy-permitted port — same pattern as backend_container_test.
	ns.CreateConfigMap(t, ctx, portCM, map[string]string{"PORT": strconv.Itoa(port)})

	ns.CreateContainerFunction(t, ctx, framework.ContainerFunctionOptions{
		Name:       fnName,
		Image:      image,
		Port:       port,
		ConfigMaps: []string{portCM},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	// The container image owns its body, so assert on status only (2xx proves
	// the executor built the Deployment/Service and the router reached it).
	f.Router(t).GetEventually(t, ctx, routePath, is2xx)

	t.Run("scale_change_reconciles_hpa", func(t *testing.T) {
		ns.CLI(t, ctx, "fn", "update-container", "--name", fnName,
			"--minscale", "1", "--maxscale", "3")

		hpa := ns.WaitForFunctionHPA(t, ctx, fnName, func(h *autoscalingv2.HorizontalPodAutoscaler) bool {
			return h.Spec.MinReplicas != nil && *h.Spec.MinReplicas == 1 && h.Spec.MaxReplicas == 3
		}, "MinReplicas=1, MaxReplicas=3", 90*time.Second)
		require.NotNil(t, hpa.Spec.MinReplicas, "HPA MinReplicas")
		assert.Equal(t, int32(1), *hpa.Spec.MinReplicas, "HPA MinReplicas")
		assert.Equal(t, int32(3), hpa.Spec.MaxReplicas, "HPA MaxReplicas")

		f.Router(t).GetEventually(t, ctx, routePath, is2xx)
	})

	t.Run("configmap_change_rewrites_deployment", func(t *testing.T) {
		extraCM := "ctrupd-extra-" + ns.ID
		ns.CreateConfigMap(t, ctx, extraCM, map[string]string{"EXTRA_KEY": "extra-value"})

		// Re-pass the PORT configmap (replace semantics) plus the new one.
		ns.CLI(t, ctx, "fn", "update-container", "--name", fnName,
			"--configmap", portCM, "--configmap", extraCM)

		// updateFuncDeployment must surface the new configmap as an EnvFrom
		// source on the live Deployment's container.
		ns.WaitForFunctionDeployment(t, ctx, fnName, func(d *appsv1.Deployment) bool {
			return deploymentReferencesConfigMaps(d, portCM, extraCM)
		}, "Deployment EnvFrom references both configmaps", 90*time.Second)

		f.Router(t).GetEventually(t, ctx, routePath, is2xx)
	})
}

// is2xx is a framework.ResponseCheck that passes on any 2xx status, ignoring the
// body (container images own their response bodies).
func is2xx(status int, _ string) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

// deploymentReferencesConfigMaps reports whether every named configmap appears
// as an EnvFrom ConfigMapRef on at least one of the Deployment's containers.
func deploymentReferencesConfigMaps(d *appsv1.Deployment, names ...string) bool {
	refs := map[string]bool{}
	for _, ctr := range d.Spec.Template.Spec.Containers {
		for _, src := range ctr.EnvFrom {
			if src.ConfigMapRef != nil {
				refs[src.ConfigMapRef.Name] = true
			}
		}
	}
	for _, n := range names {
		if !refs[n] {
			return false
		}
	}
	return true
}
