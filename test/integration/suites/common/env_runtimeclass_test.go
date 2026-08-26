// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestEnvRuntimeClassCELRoundTrip proves that EnvironmentSpec.RuntimeClassName's
// struct-level CEL rule (the +kubebuilder:validation:XValidation marker on
// EnvironmentSpec in pkg/apis/core/v1/types.go) is actually served by the
// Environment CRD installed in this cluster — not merely present in the Go
// type or the Go-side EnvironmentSpec.Validate() mirror.
//
// There is no CI gate that catches a forgotten `make generate-crds` (see
// reference_no_crd_drift_gate): crds/v1/fission.io_environments.yaml can
// silently fall behind types.go and CI stays green. This test is the real
// backstop — it only passes if the regenerated CRD (with the CEL rule) is the
// one the test cluster's apiserver actually applied.
func TestEnvRuntimeClassCELRoundTrip(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	// This test creates and reads Environment CRs only (it never materializes a
	// pod), so it must NOT gate on NODE_RUNTIME_IMAGE via RequireNode: this is
	// the load-bearing backstop that the served CRD carries the CEL rule (there
	// is no CRD-drift CI gate), and it must run even when the image env var is
	// unset. Runtime.Image just needs to be a non-empty valid ref to satisfy
	// EnvironmentSpec admission; a placeholder that never gets pulled is fine.
	image := "example.com/placeholder:latest"
	ns := f.NewTestNamespace(t)

	// A valid DNS1123-label value is admitted and round-trips.
	validName := "rcvalid-" + ns.ID
	validEnv := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: validName},
		Spec: fv1.EnvironmentSpec{
			Version: 3,
			Runtime: fv1.Runtime{
				Image:     image,
				Container: &corev1.Container{Name: validName},
			},
			RuntimeClassName: new("gvisor"),
		},
	}
	ns.CreateEnvObject(t, ctx, validEnv)

	got, err := ns.Framework().FissionClient().CoreV1().Environments(ns.Name).Get(ctx, validName, metav1.GetOptions{})
	require.NoErrorf(t, err, "get environment %q", validName)
	require.NotNil(t, got.Spec.RuntimeClassName, "RuntimeClassName should have round-tripped")
	assert.Equal(t, "gvisor", *got.Spec.RuntimeClassName)

	// An invalid value (uppercase + underscore + '!', none of which the
	// DNS1123-label regex allows) is rejected by the apiserver at admission —
	// before it ever reaches this environment's controllers.
	invalidName := "rcinvalid-" + ns.ID
	invalidEnv := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: invalidName},
		Spec: fv1.EnvironmentSpec{
			Version: 3,
			Runtime: fv1.Runtime{
				Image:     image,
				Container: &corev1.Container{Name: invalidName},
			},
			RuntimeClassName: new("Bad_Name!"),
		},
	}
	_, err = ns.Framework().FissionClient().CoreV1().Environments(ns.Name).Create(ctx, invalidEnv, metav1.CreateOptions{})
	require.Error(t, err, "expected apiserver CEL rejection of an invalid runtimeClassName")
	assert.Contains(t, err.Error(), "spec.runtimeClassName must be a valid DNS1123 label",
		"error should carry the CEL rule's message (proves the CEL rule, not just Go-side validation, rejected it)")
}

// TestEnvRuntimeClassReachesPoolDeployment proves env.Spec.RuntimeClassName
// reaches the poolmgr warm-pool Deployment's pod template — the fill-if-nil
// application in gp_deployment.go's genDeploymentSpec (via
// pkg/executor/util.ApplyEnvRuntimeClass), exercised end-to-end through a real
// Environment CR rather than a unit test's in-process Deployment build.
//
// This deliberately stops at the Deployment object: kind has no gVisor/Kata
// RuntimeClass installed, so a "wait until pod Running" assertion would hang
// forever (kind's scheduler leaves such pods Pending). Reading the created
// Deployment's pod template via the API is sufficient to prove the field
// reached the pod spec; Task 1's controller-level tests already cover the
// fill-if-nil / precedence logic in isolation.
func TestEnvRuntimeClassReachesPoolDeployment(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	envName := "rcpool-" + ns.ID
	envObj := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: envName},
		Spec: fv1.EnvironmentSpec{
			Version:  3,
			Poolsize: 1,
			Runtime: fv1.Runtime{
				Image:     image,
				Container: &corev1.Container{Name: envName},
			},
			// Deliberately no Runtime.PodSpec: this pins the "common case"
			// the plan called out — the fill-if-nil must run unconditionally,
			// not only inside a `Runtime.PodSpec != nil` branch.
			RuntimeClassName: new("gvisor"),
		},
	}
	ns.CreateEnvObject(t, ctx, envObj)

	// poolmgr creates the pool Deployment as soon as it observes the
	// Environment (see gp.go's GenericPool setup calling
	// createPoolDeployment) — no function or trigger is needed. We only wait
	// for the Deployment object to exist with the RuntimeClassName set on its
	// pod template; we never wait on replica readiness, since the referenced
	// RuntimeClass has no node handler in kind and the pods would stay
	// Pending indefinitely.
	deployment := ns.WaitForPoolDeployment(t, ctx, envName, func(d *appsv1.Deployment) bool {
		return d.Spec.Template.Spec.RuntimeClassName != nil
	}, "pod template RuntimeClassName set", 2*time.Minute)

	require.NotNil(t, deployment.Spec.Template.Spec.RuntimeClassName)
	assert.Equal(t, "gvisor", *deployment.Spec.Template.Spec.RuntimeClassName)
}
