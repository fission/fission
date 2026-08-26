// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// ApplyEnvRuntimeClass fills podSpec.RuntimeClassName from
// env.Spec.RuntimeClassName, but ONLY when podSpec.RuntimeClassName is still
// nil. It is fill-if-nil, not override: Runtime.PodSpec / Builder.PodSpec is
// the documented full-override escape hatch, and MergePodSpec already
// propagates a RuntimeClassName set there onto podSpec before this runs — so
// a pod spec that already named a RuntimeClass keeps it, and the dedicated
// field only supplies a default when nothing more specific was given.
//
// Callers MUST invoke this unconditionally, after any MergePodSpec call and
// OUTSIDE the "if env.Spec.Runtime.PodSpec != nil" (or Builder.PodSpec)
// guard: an environment can set RuntimeClassName with no PodSpec override at
// all (the common case), and such an environment never enters that guard.
// Placing this call inside it would silently drop the field.
func ApplyEnvRuntimeClass(podSpec *apiv1.PodSpec, env *fv1.Environment) {
	if env.Spec.RuntimeClassName != nil && podSpec.RuntimeClassName == nil {
		podSpec.RuntimeClassName = env.Spec.RuntimeClassName
	}
}
