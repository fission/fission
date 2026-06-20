// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// desiredService builds the Service a caller would hand to CreateOrAdoptService:
// managed-by label set, instanceID annotation set, a concrete selector/ports.
func desiredService(name, instanceID string) *apiv1.Service {
	return &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE,
				"app":                "desired",
			},
			Annotations: map[string]string{
				fv1.EXECUTOR_INSTANCEID_LABEL: instanceID,
			},
		},
		Spec: apiv1.ServiceSpec{
			Selector: map[string]string{"fn": name},
			Type:     apiv1.ServiceTypeClusterIP,
			Ports:    []apiv1.ServicePort{{Name: "http-env", Port: 80}},
		},
	}
}

func TestCreateOrAdoptService(t *testing.T) {
	t.Parallel()
	const ns, name, instanceID = "fission", "fn-svc", "inst-1"

	t.Run("creates when absent", func(t *testing.T) {
		t.Parallel()
		client := fake.NewClientset()
		got, created, err := CreateOrAdoptService(t.Context(), client, logr.Discard(), instanceID, ns, desiredService(name, instanceID))
		require.NoError(t, err)
		assert.True(t, created, "absent service should take the create path")
		require.NotNil(t, got)
		assert.Equal(t, name, got.Name)
		assert.Equal(t, fv1.MANAGED_BY_VALUE, got.Labels[fv1.MANAGED_BY_LABEL])
	})

	t.Run("no drift returns existing untouched", func(t *testing.T) {
		t.Parallel()
		existing := desiredService(name, instanceID) // already managed by this instance
		existing.Namespace = ns
		existing.Labels["sentinel"] = "keep" // adoption would replace Labels wholesale
		client := fake.NewClientset(existing)

		got, created, err := CreateOrAdoptService(t.Context(), client, logr.Discard(), instanceID, ns, desiredService(name, instanceID))
		require.NoError(t, err)
		assert.False(t, created, "an in-sync service must not be (re)created")
		require.NotNil(t, got)
		assert.Equal(t, "keep", got.Labels["sentinel"], "no-drift path must not overwrite the live object")
	})

	t.Run("adopts orphan missing managed-by label", func(t *testing.T) {
		t.Parallel()
		orphan := &apiv1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns,
				Labels:      map[string]string{"app": "old"}, // no managed-by label -> orphan
				Annotations: map[string]string{fv1.EXECUTOR_INSTANCEID_LABEL: instanceID},
			},
			Spec: apiv1.ServiceSpec{Selector: map[string]string{"fn": "stale"}, Type: apiv1.ServiceTypeClusterIP},
		}
		client := fake.NewClientset(orphan)

		got, created, err := CreateOrAdoptService(t.Context(), client, logr.Discard(), instanceID, ns, desiredService(name, instanceID))
		require.NoError(t, err)
		assert.False(t, created, "adoption is an Update, not a Create")
		require.NotNil(t, got)
		assert.Equal(t, fv1.MANAGED_BY_VALUE, got.Labels[fv1.MANAGED_BY_LABEL], "orphan should gain the managed-by label")
		assert.Equal(t, map[string]string{"fn": name}, got.Spec.Selector, "spec selector should be overwritten from desired")
	})

	t.Run("adopts service owned by a different instance", func(t *testing.T) {
		t.Parallel()
		orphan := desiredService(name, "other-instance") // managed-by set, but foreign instance id
		orphan.Namespace = ns
		orphan.Spec.Selector = map[string]string{"fn": "stale"}
		client := fake.NewClientset(orphan)

		got, created, err := CreateOrAdoptService(t.Context(), client, logr.Discard(), instanceID, ns, desiredService(name, instanceID))
		require.NoError(t, err)
		assert.False(t, created)
		require.NotNil(t, got)
		assert.Equal(t, map[string]string{"fn": name}, got.Spec.Selector, "foreign-instance service should be re-adopted to desired spec")
	})
}
