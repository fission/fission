// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
// helper enumerates every populated PodSpec field that is not on the
// allowlist above, so admission errors and audit logs cover the same
// surface the merge helper silently drops.
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

// DisallowedPodSpecFields returns the deduplicated list of populated
// PodSpec field names in `ps` that are NOT on the
// MessageQueueTrigger.Spec.PodSpec allowlist. The pod-level allowlist is
// NodeSelector, Tolerations, Affinity, and RuntimeClassName; the
// per-container allowlist is Name (metadata, used for the per-container
// Resources match) and Resources itself. Every other settable field is
// reported here so the admission webhook can fail loudly on the same
// surface that MergeAllowedPodSpecFields would otherwise silently drop.
//
// This is the single source of truth shared by the controller-side merge
// in MergeAllowedPodSpecFields and the admission webhook in
// pkg/webhook/messagequeuetrigger.go — extend this function (and only
// this function) when changing what the webhook rejects.
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

	// Pod-level fields. Enumerate every populated PodSpec field other than
	// the pod-level allowlist (NodeSelector, Tolerations, Affinity,
	// RuntimeClassName) so that admission errors match what the merge helper
	// would silently drop.
	if len(ps.Volumes) > 0 {
		add("volumes")
	}
	if len(ps.InitContainers) > 0 {
		add("initContainers")
	}
	if len(ps.EphemeralContainers) > 0 {
		add("ephemeralContainers")
	}
	if ps.RestartPolicy != "" {
		add("restartPolicy")
	}
	if ps.TerminationGracePeriodSeconds != nil {
		add("terminationGracePeriodSeconds")
	}
	if ps.ActiveDeadlineSeconds != nil {
		add("activeDeadlineSeconds")
	}
	if ps.DNSPolicy != "" {
		add("dnsPolicy")
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
	if ps.Hostname != "" {
		add("hostname")
	}
	if ps.Subdomain != "" {
		add("subdomain")
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
	if ps.Priority != nil {
		add("priority")
	}
	if ps.DNSConfig != nil {
		add("dnsConfig")
	}
	if len(ps.ReadinessGates) > 0 {
		add("readinessGates")
	}
	if ps.EnableServiceLinks != nil {
		add("enableServiceLinks")
	}
	if ps.PreemptionPolicy != nil {
		add("preemptionPolicy")
	}
	if len(ps.Overhead) > 0 {
		add("overhead")
	}
	if len(ps.TopologySpreadConstraints) > 0 {
		add("topologySpreadConstraints")
	}
	if ps.SetHostnameAsFQDN != nil {
		add("setHostnameAsFQDN")
	}
	if ps.OS != nil {
		add("os")
	}
	if ps.HostUsers != nil {
		add("hostUsers")
	}
	if len(ps.SchedulingGates) > 0 {
		add("schedulingGates")
	}
	if len(ps.ResourceClaims) > 0 {
		add("resourceClaims")
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
