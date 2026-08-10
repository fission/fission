// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
)

// TestAddFetcherToPodSpecPreStopLifecycle asserts that addFetcherToPodSpecWithCommand
// (exercised through the exported AddFetcherToPodSpec entry point) sets a
// kubelet-native Sleep preStop hook on the fetcher sidecar container when the
// pod's TerminationGracePeriodSeconds is positive, and nil Lifecycle when it
// is zero. The fetcher image is distroless (chainguard/static) and has no
// /bin/sleep binary, so an exec-based hook would fail on every termination.
func TestAddFetcherToPodSpecPreStopLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("positive grace period uses native sleep", func(t *testing.T) {
		t.Parallel()
		cfg, err := MakeFetcherConfig("/userfunc")
		require.NoError(t, err)

		grace := int64(360)
		podSpec := &apiv1.PodSpec{
			Containers: []apiv1.Container{
				{Name: "user"},
			},
			TerminationGracePeriodSeconds: &grace,
		}

		err = cfg.AddFetcherToPodSpec(podSpec, "user", "test-ns")
		require.NoError(t, err)

		// Locate the fetcher sidecar that was injected.
		var fetcherContainer *apiv1.Container
		for i := range podSpec.Containers {
			if podSpec.Containers[i].Name == "fetcher" {
				fetcherContainer = &podSpec.Containers[i]
				break
			}
		}
		require.NotNil(t, fetcherContainer, "fetcher container must be injected into the pod spec")
		require.NotNil(t, fetcherContainer.Lifecycle, "fetcher Lifecycle must be set")
		require.NotNil(t, fetcherContainer.Lifecycle.PreStop, "fetcher Lifecycle.PreStop must be set")

		preStop := fetcherContainer.Lifecycle.PreStop
		assert.Nil(t, preStop.Exec, "PreStop.Exec must be nil — fetcher image has no /bin/sleep")
		require.NotNil(t, preStop.Sleep, "PreStop.Sleep must be set for kubelet-native drain")
		assert.Equal(t, int64(360), preStop.Sleep.Seconds,
			"PreStop.Sleep.Seconds must equal the pod's TerminationGracePeriodSeconds")
	})

	t.Run("zero grace period produces nil lifecycle", func(t *testing.T) {
		t.Parallel()
		cfg, err := MakeFetcherConfig("/userfunc")
		require.NoError(t, err)

		grace := int64(0)
		podSpec := &apiv1.PodSpec{
			Containers: []apiv1.Container{
				{Name: "user"},
			},
			TerminationGracePeriodSeconds: &grace,
		}

		err = cfg.AddFetcherToPodSpec(podSpec, "user", "test-ns")
		require.NoError(t, err)

		var fetcherContainer *apiv1.Container
		for i := range podSpec.Containers {
			if podSpec.Containers[i].Name == "fetcher" {
				fetcherContainer = &podSpec.Containers[i]
				break
			}
		}
		require.NotNil(t, fetcherContainer, "fetcher container must be injected into the pod spec")
		assert.Nil(t, fetcherContainer.Lifecycle,
			"fetcher Lifecycle must be nil when grace=0 (no drain window; Sleep.Seconds>=1 required by the API)")
	})

	t.Run("nil grace period leaves lifecycle unset", func(t *testing.T) {
		t.Parallel()
		cfg, err := MakeFetcherConfig("/userfunc")
		require.NoError(t, err)

		podSpec := &apiv1.PodSpec{
			Containers: []apiv1.Container{
				{Name: "user"},
			},
			TerminationGracePeriodSeconds: nil,
		}

		err = cfg.AddFetcherToPodSpec(podSpec, "user", "test-ns")
		require.NoError(t, err)

		var fetcherContainer *apiv1.Container
		for i := range podSpec.Containers {
			if podSpec.Containers[i].Name == "fetcher" {
				fetcherContainer = &podSpec.Containers[i]
				break
			}
		}
		require.NotNil(t, fetcherContainer, "fetcher container must be injected into the pod spec")
		assert.Nil(t, fetcherContainer.Lifecycle,
			"fetcher Lifecycle must be nil when TerminationGracePeriodSeconds is nil")
	})
}

// TestAddFetcherToPodSpecPodServiceAccountEnv pins the RFC-0031 keychain
// identity fix: the fetcher container carries a downward-API env var with
// the pod's ACTUAL ServiceAccount, so the OCI keychain resolves the builder
// SA in builder pods instead of hardcoding fission-fetcher.
func TestAddFetcherToPodSpecPodServiceAccountEnv(t *testing.T) {
	t.Parallel()
	cfg, err := MakeFetcherConfig("/userfunc")
	require.NoError(t, err)

	podSpec := &apiv1.PodSpec{
		Containers:         []apiv1.Container{{Name: "user"}},
		ServiceAccountName: "fission-builder",
	}
	require.NoError(t, cfg.AddFetcherToPodSpec(podSpec, "user", "test-ns"))

	var fetcherContainer *apiv1.Container
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == "fetcher" {
			fetcherContainer = &podSpec.Containers[i]
			break
		}
	}
	require.NotNil(t, fetcherContainer)
	var found *apiv1.EnvVar
	for i := range fetcherContainer.Env {
		if fetcherContainer.Env[i].Name == "POD_SERVICE_ACCOUNT" {
			found = &fetcherContainer.Env[i]
			break
		}
	}
	require.NotNil(t, found, "POD_SERVICE_ACCOUNT env var must be set on the fetcher container")
	require.NotNil(t, found.ValueFrom, "must come from the downward API, not a literal")
	require.NotNil(t, found.ValueFrom.FieldRef)
	assert.Equal(t, "spec.serviceAccountName", found.ValueFrom.FieldRef.FieldPath,
		"downward API stays correct regardless of who sets the SA, before or after this call")
	// The pre-set SA must survive (config only defaults an empty one).
	assert.Equal(t, "fission-builder", podSpec.ServiceAccountName)
}
