// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// ApplyFunctionEnv applies a function's per-function env (RFC-0030 §1/§2) to
// the named runtime container of an already-merged pod spec. It must run
// AFTER every MergeContainer/MergePodSpec call so no later merge can reorder
// the result. No-op when the function declares neither Env nor EnvFrom, which
// keeps pre-RFC pod specs byte-identical (E6).
//
// Ordering delivers the guarantee (not validation): the container's existing
// env — the environment podspec's merged entries plus the platform vars the
// builder placed there — is stripped of platform names, then the function's
// Env is appended, then the platform vars are re-appended LAST. The kubelet
// resolves duplicate env names last-wins and gives literal env precedence
// over envFrom, so platform names win unconditionally over both user literals
// and EnvFrom-expanded keys, while the function's own keys win over its
// environment's podspec (env podspec < EnvFrom < Env, per the RFC's merge
// precedence). Function EnvFrom sources append after any existing sources,
// so later-wins matches the declared order.
func ApplyFunctionEnv(podSpec *apiv1.PodSpec, containerName string, fn *fv1.Function, platform []apiv1.EnvVar) {
	if len(fn.Spec.Env) == 0 && len(fn.Spec.EnvFrom) == 0 {
		return
	}
	platformNames := make(map[string]struct{}, len(platform))
	for _, p := range platform {
		platformNames[p.Name] = struct{}{}
	}
	for i := range podSpec.Containers {
		c := &podSpec.Containers[i]
		if c.Name != containerName {
			continue
		}
		kept := make([]apiv1.EnvVar, 0, len(c.Env)+len(fn.Spec.Env)+len(platform))
		for _, e := range c.Env {
			if _, isPlatform := platformNames[e.Name]; !isPlatform {
				kept = append(kept, e)
			}
		}
		kept = append(kept, fn.Spec.Env...)
		kept = append(kept, platform...)
		c.Env = kept
		c.EnvFrom = append(c.EnvFrom, fn.Spec.EnvFrom...)
		return
	}
}
