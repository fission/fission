// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestEnvRuntimeHashGolden pins the exact bytes-level output of
// envRuntimeHash. The hash is stamped on pods as ENVIRONMENT_RUNTIME_HASH and
// compared against live objects across executor restarts and upgrades: if the
// value moves for an unchanged Environment, every warm pod in every pool is
// recycled on the release that shipped the change (see the contract comment on
// envRuntimeHash). This test exists so that any change to the hash input set,
// to struct field order, or to the JSON marshaler feeding sha256 — including a
// future encoding/json/v2 migration — fails loudly here instead of shipping a
// silent cluster-wide pool roll.
//
// If this test fails, DO NOT just update the constants: decide first whether a
// one-time pool roll is acceptable for the release at hand, and release-note it.
func TestEnvRuntimeHashGolden(t *testing.T) {
	t.Parallel()

	grace := int64(30)
	full := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "golden-env",
			Namespace:   "default",
			UID:         "0f7bb1a2-8b52-4a45-93a5-2f2f7a1d2c3e",
			Labels:      map[string]string{"team": "b", "app": "a"},
			Annotations: map[string]string{"note": "x"},
		},
		Spec: fv1.EnvironmentSpec{
			Version: 3,
			Runtime: fv1.Runtime{
				Image: "fission/python-env:1.2.3",
			},
			Resources: apiv1.ResourceRequirements{
				Requests: apiv1.ResourceList{
					apiv1.ResourceCPU:    resource.MustParse("100m"),
					apiv1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
			TerminationGracePeriod:       &grace,
			ImagePullSecret:              "pull-secret",
			AllowAccessToExternalNetwork: true,
		},
	}
	zero := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "zero-env"},
		Spec: fv1.EnvironmentSpec{
			Runtime: fv1.Runtime{Image: "fission/node-env"},
		},
	}

	tests := []struct {
		name string
		env  *fv1.Environment
		want string
	}{
		// Golden values captured on go1.27.0 (encoding/json v1 semantics).
		{name: "fully populated", env: full, want: "a589fc617b4e5c1f"},
		{name: "zero-heavy (nil maps, defaulted grace)", env: zero, want: "00d572a8889a85c3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := envRuntimeHash(tt.env)
			require.Len(t, got, 16)
			assert.Equal(t, tt.want, got)
			// Determinism across calls in one process.
			assert.Equal(t, got, envRuntimeHash(tt.env))
		})
	}
}
