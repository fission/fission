// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func liveFunctionForIdentity() *fv1.Function {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "hello",
			Namespace:  "default",
			UID:        types.UID("fn-uid-1"),
			Generation: 5,
			Labels: map[string]string{
				"already": "here",
			},
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "nodejs", Namespace: "default"},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{Name: "hello-pkg", Namespace: "default"},
			},
			Versioning: &fv1.VersioningConfig{Mode: fv1.VersioningMode("auto")},
		},
	}
	return fn
}

func versionForIdentity(fn *fv1.Function, seq int64, generation int64) *fv1.FunctionVersion {
	snap := *fn.Spec.DeepCopy()
	snap.Versioning = nil
	// The snapshot's package name diverges from live's so a test that
	// compares Spec fields catches an accidental copy of live.Spec instead
	// of v.Spec.Snapshot.
	snap.Package.PackageRef.Name = "hello-v-snapshot-pkg"
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello-v2",
			Namespace: fn.Namespace,
		},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:       fn.Name,
			FunctionUID:        fn.UID,
			FunctionGeneration: generation,
			Sequence:           seq,
			Snapshot:           snap,
		},
	}
}

func TestVersionedFunction(t *testing.T) {
	t.Parallel()

	t.Run("spec becomes the version's snapshot, deep-copied", func(t *testing.T) {
		t.Parallel()
		live := liveFunctionForIdentity()
		v := versionForIdentity(live, 2, 6)

		got := VersionedFunction(live, v)

		assert.Equal(t, v.Spec.Snapshot, got.Spec, "Spec must equal the version's snapshot")
		assert.NotEqual(t, live.Spec, got.Spec, "Spec must NOT be live's spec (snapshot diverges via PackageRef.Name)")

		// Mutating the returned Spec must not reach back into the version's
		// snapshot -- confirms a deep copy, not an aliased sub-struct.
		got.Spec.Package.PackageRef.Name = "mutated"
		assert.Equal(t, "hello-v-snapshot-pkg", v.Spec.Snapshot.Package.PackageRef.Name,
			"mutating the returned Function must not mutate the version's snapshot")
	})

	t.Run("generation is pinned from the version, not live", func(t *testing.T) {
		t.Parallel()
		live := liveFunctionForIdentity()
		require.Equal(t, int64(5), live.Generation)
		v := versionForIdentity(live, 2, 6)

		got := VersionedFunction(live, v)

		assert.Equal(t, int64(6), got.Generation, "Generation must be the version's FunctionGeneration, not live's")
	})

	t.Run("version label is set without mutating live's label map", func(t *testing.T) {
		t.Parallel()
		live := liveFunctionForIdentity()
		v := versionForIdentity(live, 2, 6)

		got := VersionedFunction(live, v)

		assert.Equal(t, "hello-v2", got.Labels[fv1.FUNCTION_VERSION])
		assert.Equal(t, "here", got.Labels["already"], "pre-existing labels must survive")
		_, present := live.Labels[fv1.FUNCTION_VERSION]
		assert.False(t, present, "live's label map must never gain the version label")

		// Mutate the returned map and confirm live is unaffected (copy-on-write).
		got.Labels["poke"] = "poked"
		_, present = live.Labels["poke"]
		assert.False(t, present, "mutating the returned Function's labels must not mutate live's map")
	})

	t.Run("nil live labels do not panic and still get the version label", func(t *testing.T) {
		t.Parallel()
		live := liveFunctionForIdentity()
		live.Labels = nil
		v := versionForIdentity(live, 2, 6)

		got := VersionedFunction(live, v)

		require.NotNil(t, got.Labels)
		assert.Equal(t, "hello-v2", got.Labels[fv1.FUNCTION_VERSION])
		assert.Nil(t, live.Labels, "live must remain untouched")
	})

	t.Run("identity metadata (name, namespace, UID) is preserved from live", func(t *testing.T) {
		t.Parallel()
		live := liveFunctionForIdentity()
		v := versionForIdentity(live, 2, 6)

		got := VersionedFunction(live, v)

		assert.Equal(t, live.Name, got.Name)
		assert.Equal(t, live.Namespace, got.Namespace)
		assert.Equal(t, live.UID, got.UID)
	})

	t.Run("live is untouched by the call", func(t *testing.T) {
		t.Parallel()
		live := liveFunctionForIdentity()
		liveBefore := live.DeepCopy()
		v := versionForIdentity(live, 2, 6)

		_ = VersionedFunction(live, v)

		assert.Equal(t, liveBefore, live, "VersionedFunction must never mutate its live argument")
	})
}
