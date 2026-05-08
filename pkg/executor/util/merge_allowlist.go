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

	var dropped []string

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

	// --- Audit drops ---

	for i := range user.Containers {
		c := user.Containers[i]
		if c.Image != "" {
			dropped = append(dropped, "containers[].image")
		}
		if len(c.Command) > 0 {
			dropped = append(dropped, "containers[].command")
		}
		if len(c.Args) > 0 {
			dropped = append(dropped, "containers[].args")
		}
		if len(c.Env) > 0 {
			dropped = append(dropped, "containers[].env")
		}
		if len(c.VolumeMounts) > 0 {
			dropped = append(dropped, "containers[].volumeMounts")
		}
	}
	if len(user.Volumes) > 0 {
		dropped = append(dropped, "volumes")
	}
	if user.ServiceAccountName != "" {
		dropped = append(dropped, "serviceAccountName")
	}
	if user.HostNetwork {
		dropped = append(dropped, "hostNetwork")
	}
	if user.HostPID {
		dropped = append(dropped, "hostPID")
	}
	if user.HostIPC {
		dropped = append(dropped, "hostIPC")
	}
	if user.SecurityContext != nil && user.SecurityContext.RunAsUser != nil {
		dropped = append(dropped, "securityContext.runAsUser")
	}

	return out, dropped, nil
}
