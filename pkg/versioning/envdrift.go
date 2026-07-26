// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// SnapshotEnvNamespace returns the namespace of the environment a
// FunctionVersion's snapshot references: the snapshot's explicit env
// namespace, or the function's namespace when unset. THE definition of the
// envNS fallback -- callers must not re-derive it.
func SnapshotEnvNamespace(v *fv1.FunctionVersion, fnNamespace string) string {
	envNS := v.Spec.Snapshot.Environment.Namespace
	if envNS == "" {
		envNS = fnNamespace
	}
	return envNS
}

// EnvDrifted reports whether env has moved past the generation the version
// observed at publish time. THE definition of env drift -- callers must not
// re-derive it.
func EnvDrifted(v *fv1.FunctionVersion, env *fv1.Environment) bool {
	return v.Spec.EnvObservedGeneration != env.Generation
}
