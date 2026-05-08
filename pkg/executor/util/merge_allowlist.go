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
// onto a deep copy of `src`. The merge uses allowlist semantics: only the
// fields explicitly listed below are ever copied from `user` to the result.
// Every other PodSpec field present in `user` is silently dropped, regardless
// of whether DisallowedPodSpecFields enumerates it. The returned PodSpec is
// always a fresh deep copy; `src` is not mutated.
//
// Allowed (everything else is dropped):
//   - NodeSelector
//   - Tolerations
//   - Affinity
//   - RuntimeClassName
//   - Containers[i].Resources, when the container name matches a
//     controller-built container
//
// The slice of dropped field names returned alongside the merged spec is
// produced by DisallowedPodSpecFields for caller-side audit logging. That
// helper enumerates the high-impact subset of disallowed fields so the
// admission webhook can fail loudly on them; lower-impact fields (e.g.
// Hostname, Subdomain, EnableServiceLinks) are still dropped by the merge
// but not surfaced.
//
// This is the controller-side defence for GHSA-7m8x-qg2j-4m3v; the
// validating webhook in pkg/webhook/messagequeuetrigger.go rejects the same
// disallowed fields at admission time.
func MergeAllowedPodSpecFields(src, user *apiv1.PodSpec) (*apiv1.PodSpec, []string) {
	if src == nil {
		return nil, nil
	}
	out := src.DeepCopy()
	if user == nil {
		return out, nil
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

	return out, DisallowedPodSpecFields(user)
}

// DisallowedPodSpecFields returns the deduplicated list of high-impact
// PodSpec field names that appear in `ps` but are NOT on the
// MessageQueueTrigger.Spec.PodSpec allowlist. This is the single source of
// truth shared by the controller-side merge in MergeAllowedPodSpecFields
// and the admission webhook in pkg/webhook/messagequeuetrigger.go — extend
// this function (and only this function) when changing what the webhook
// rejects.
//
// Note that the merge helper applies allowlist semantics independently of
// this enumeration: any user-supplied PodSpec field not in the merge
// helper's allowlist is silently dropped at controller time, even if not
// listed below. This function exists to surface the most security-relevant
// disallowed fields at admission time so users get a clear error rather
// than silent field loss.
//
// The returned names use the JSON form without a leading "spec.podspec."
// prefix; callers that surface the names in user-facing errors should
// prepend their own prefix.
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

	// Pod-level fields. Each of these is a credible attack surface for a
	// caller that can create MessageQueueTriggers but would not normally
	// have permission to create the underlying Kubernetes objects.
	if len(ps.Volumes) > 0 {
		add("volumes")
	}
	if len(ps.InitContainers) > 0 {
		add("initContainers")
	}
	if len(ps.EphemeralContainers) > 0 {
		add("ephemeralContainers")
	}
	if ps.ServiceAccountName != "" {
		add("serviceAccountName")
	}
	if ps.AutomountServiceAccountToken != nil {
		add("automountServiceAccountToken")
	}
	if ps.NodeName != "" {
		add("nodeName")
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
	if ps.ShareProcessNamespace != nil {
		add("shareProcessNamespace")
	}
	if ps.SecurityContext != nil {
		add("securityContext")
	}
	if len(ps.ImagePullSecrets) > 0 {
		add("imagePullSecrets")
	}
	if ps.SchedulerName != "" {
		add("schedulerName")
	}
	if len(ps.HostAliases) > 0 {
		add("hostAliases")
	}
	if ps.PriorityClassName != "" {
		add("priorityClassName")
	}
	if ps.DNSConfig != nil {
		add("dnsConfig")
	}
	if len(ps.TopologySpreadConstraints) > 0 {
		add("topologySpreadConstraints")
	}

	// Container-level fields. Within a container the only allowlisted
	// fields are Name (used for the per-container Resources match) and
	// Resources itself. Everything else flagged here would let a caller
	// run arbitrary code or exfiltrate data through the connector pod.
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
		if len(c.EnvFrom) > 0 {
			add("containers[].envFrom")
		}
		if len(c.VolumeMounts) > 0 {
			add("containers[].volumeMounts")
		}
		if len(c.VolumeDevices) > 0 {
			add("containers[].volumeDevices")
		}
		if len(c.Ports) > 0 {
			add("containers[].ports")
		}
		if c.WorkingDir != "" {
			add("containers[].workingDir")
		}
		if c.Lifecycle != nil {
			add("containers[].lifecycle")
		}
		if c.LivenessProbe != nil {
			add("containers[].livenessProbe")
		}
		if c.ReadinessProbe != nil {
			add("containers[].readinessProbe")
		}
		if c.StartupProbe != nil {
			add("containers[].startupProbe")
		}
		if c.SecurityContext != nil {
			add("containers[].securityContext")
		}
	}
	return bad
}
