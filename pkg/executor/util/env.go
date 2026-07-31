// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"

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
// and EnvFrom-expanded keys, and the function's Env wins over its
// environment's podspec.
//
// The EnvFrom tier is weaker than the RFC's end-state precedence, and the gap
// is inherent to native injection rather than an oversight: the kubelet
// expands envFrom before container env, so a literal set by the environment
// podspec still beats an EnvFrom-supplied key of the same name, and this
// function cannot strip what it cannot enumerate (an object's data keys are
// not knowable at pod-build time). Function EnvFrom sources append after any
// existing sources, so later-wins holds among envFrom sources themselves.
// Full precedence lands with the poolmgr phase, which resolves values itself.
//
// It returns an error when the function declares env but containerName is not
// in the pod spec: silently dropping a declared DATABASE_URL is the failure
// mode this whole feature exists to prevent, so a name mismatch must be loud.
//
// podNamespace is the namespace the pod is created in. Env object references
// are LocalObjectReferences, so the kubelet resolves them relative to the POD,
// while Fission's rotation bookkeeping (the cms watcher index and
// ReferencedResourcesRVSum) keys on the Function CR's namespace. In the
// deprecated split-namespace install (functionNamespace set) those differ, and
// the mismatch would both read an object the tenant may not own and leave
// rotation silently ineffective — so references are refused there rather than
// half-supported. Literal env vars resolve nothing and stay allowed.
func ApplyFunctionEnv(podSpec *apiv1.PodSpec, containerName, podNamespace string, fn *fv1.Function, platform []apiv1.EnvVar) error {
	if len(fn.Spec.Env) == 0 && len(fn.Spec.EnvFrom) == 0 {
		return nil
	}
	if podNamespace != fn.Namespace &&
		(len(fn.Spec.EnvSecretNames()) > 0 || len(fn.Spec.EnvConfigMapNames()) > 0) {
		return fmt.Errorf("env valueFrom/envFrom references are not supported when function pods run in a different namespace (%q) than the function (%q): "+
			"the kubelet would resolve them in the pod namespace while rotation tracking keys on the function namespace; use literal env vars, or unset functionNamespace",
			podNamespace, fn.Namespace)
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
		// Shadowed entries are dropped rather than left for kubelet last-wins
		// to resolve: the result is the same at runtime, but a pod spec
		// without duplicate env names stays readable and safe to re-feed
		// through MergeContainer (which rejects duplicates).
		shadowed := make(map[string]struct{}, len(platform)+len(fn.Spec.Env))
		for name := range platformNames {
			shadowed[name] = struct{}{}
		}
		for _, e := range fn.Spec.Env {
			shadowed[e.Name] = struct{}{}
		}
		kept := make([]apiv1.EnvVar, 0, len(c.Env)+len(fn.Spec.Env)+len(platform))
		for _, e := range c.Env {
			if _, isShadowed := shadowed[e.Name]; !isShadowed {
				kept = append(kept, e)
			}
		}
		// User env first, platform last: kubelet last-wins makes platform
		// names authoritative even if a user literal repeats one (admission
		// denies that, but admission is UX — this ordering is the guarantee).
		for _, e := range fn.Spec.Env {
			if _, isPlatform := platformNames[e.Name]; !isPlatform {
				kept = append(kept, e)
			}
		}
		kept = append(kept, platform...)
		c.Env = kept
		c.EnvFrom = append(c.EnvFrom, fn.Spec.EnvFrom...)
		return nil
	}
	return fmt.Errorf("cannot apply function env: container %q not found in the pod spec", containerName)
}
