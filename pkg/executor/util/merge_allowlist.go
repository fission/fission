/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	apiv1 "k8s.io/api/core/v1"
)

// MergeAllowedPodSpecFields overlays a small allowlist of fields from `user`
// onto a deep copy of `src`. Fields not on the allowlist are silently
// ignored, with the dropped field names returned for caller-side audit
// logging. The returned PodSpec is always a fresh deep copy; `src` is not
// mutated.
//
// Allowed: NodeSelector, Tolerations, Affinity, RuntimeClassName, plus
// Containers[i].Resources for containers with a name match.
//
// All other user-supplied fields — notably Containers[].Image,
// Containers[].Command, Containers[].Args, Containers[].Env,
// Containers[].VolumeMounts, Volumes, ServiceAccountName,
// HostNetwork, HostPID, HostIPC, and SecurityContext.RunAsUser — are
// dropped. This is the controller-side defence for GHSA-7m8x-qg2j-4m3v;
// the validating webhook rejects the same fields at admission time.
func MergeAllowedPodSpecFields(src, user *apiv1.PodSpec) (*apiv1.PodSpec, []string, error) {
	if src == nil {
		return nil, nil, nil
	}
	out := src.DeepCopy()
	if user == nil {
		return out, nil, nil
	}

	// --- Allowlisted merges ---

	if len(user.NodeSelector) > 0 {
		if out.NodeSelector == nil {
			out.NodeSelector = make(map[string]string, len(user.NodeSelector))
		}
		for k, v := range user.NodeSelector {
			out.NodeSelector[k] = v
		}
	}
	if len(user.Tolerations) > 0 {
		out.Tolerations = append(out.Tolerations, user.Tolerations...)
	}
	if user.Affinity != nil {
		out.Affinity = user.Affinity.DeepCopy()
	}
	if user.RuntimeClassName != nil {
		rc := *user.RuntimeClassName
		out.RuntimeClassName = &rc
	}

	// Per-container Resources merge (name-matched only).
	for i := range out.Containers {
		for j := range user.Containers {
			if user.Containers[j].Name != out.Containers[i].Name {
				continue
			}
			if len(user.Containers[j].Resources.Limits) > 0 ||
				len(user.Containers[j].Resources.Requests) > 0 {
				out.Containers[i].Resources = *user.Containers[j].Resources.DeepCopy()
			}
		}
	}

	return out, DisallowedPodSpecFields(user), nil
}

// DisallowedPodSpecFields returns the deduplicated list of PodSpec field
// names that appear in `ps` but are NOT on the
// MessageQueueTrigger.Spec.PodSpec allowlist. This is the single source of
// truth shared by the controller-side merge in MergeAllowedPodSpecFields
// and the admission webhook in pkg/webhook/messagequeuetrigger.go — extend
// this function (and only this function) when changing the allowlist.
//
// The returned names use the executor-internal form (no "podSpec." prefix);
// callers that surface the names in user-facing errors should prepend their
// own prefix.
func DisallowedPodSpecFields(ps *apiv1.PodSpec) []string {
	if ps == nil {
		return nil
	}

	seen := make(map[string]struct{})
	var bad []string
	add := func(name string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		bad = append(bad, name)
	}

	for i := range ps.Containers {
		c := ps.Containers[i]
		if c.Image != "" {
			add("containers[].image")
		}
		if len(c.Command) > 0 {
			add("containers[].command")
		}
		if len(c.Args) > 0 {
			add("containers[].args")
		}
		if len(c.Env) > 0 {
			add("containers[].env")
		}
		if len(c.VolumeMounts) > 0 {
			add("containers[].volumeMounts")
		}
	}
	if len(ps.Volumes) > 0 {
		add("volumes")
	}
	if ps.ServiceAccountName != "" {
		add("serviceAccountName")
	}
	if ps.HostNetwork {
		add("hostNetwork")
	}
	if ps.HostPID {
		add("hostPID")
	}
	if ps.HostIPC {
		add("hostIPC")
	}
	if ps.SecurityContext != nil && ps.SecurityContext.RunAsUser != nil {
		add("securityContext.runAsUser")
	}
	return bad
}
