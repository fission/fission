// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"errors"
	"fmt"

	apiv1 "k8s.io/api/core/v1"
)

// allowedCapabilities is the strict allowlist of Linux capabilities a tenant
// may request via `securityContext.capabilities.add` on Environment- or
// Function-supplied (init)containers. It matches Kubernetes Pod Security
// Admission's "restricted" profile (only NET_BIND_SERVICE may be added on top
// of the forced drop: ["ALL"] applied at the executor merge layer).
//
// Replaces the previous fixed denylist of six capabilities (SYS_ADMIN,
// NET_ADMIN, SYS_PTRACE, SYS_MODULE, DAC_READ_SEARCH, DAC_OVERRIDE). The
// denylist was structurally incomplete: it omitted at least SYS_TIME (which
// lets a tenant rewrite the shared node wall clock), and could never constrain
// the capabilities the OCI runtime grants by default (the merge layer addresses
// those via drop: ["ALL"]). Closes GHSA-qf5v-m7p4-95rp.
var allowedCapabilities = map[apiv1.Capability]struct{}{
	"NET_BIND_SERVICE": {},
}

// ValidatePodSpecSafety rejects PodSpec fields that would let a low-privilege
// tenant escalate to host or cluster level when the executor or buildermgr
// schedules a pod from a user-supplied podspec.
//
// The fission-executor and fission-builder service accounts have the
// authority to create Deployments and Pods, so any field that crosses
// the container sandbox boundary (host namespaces, privileged contexts,
// hostPath mounts, alternate service accounts, dangerous capabilities)
// would let a Function- or Environment-CRUD tenant escape the boundary
// of their own RBAC and reach node-level state.
//
// Closes GHSA-gx55-f84r-v3r7, GHSA-wmgg-3p4h-48x7, GHSA-v455-mv2v-5g92.
//
// The fieldPath argument is used as a prefix in error messages so the
// caller can identify which podspec failed (e.g.
// "Environment.spec.runtime.podspec" / "Function.spec.podspec").
func ValidatePodSpecSafety(fieldPath string, ps *apiv1.PodSpec) error {
	if ps == nil {
		return nil
	}
	var errs error

	if ps.HostNetwork {
		errs = errors.Join(errs, fmt.Errorf("%s.hostNetwork is not allowed", fieldPath))
	}
	if ps.HostPID {
		errs = errors.Join(errs, fmt.Errorf("%s.hostPID is not allowed", fieldPath))
	}
	if ps.HostIPC {
		errs = errors.Join(errs, fmt.Errorf("%s.hostIPC is not allowed", fieldPath))
	}
	if ps.ServiceAccountName != "" {
		errs = errors.Join(errs, fmt.Errorf("%s.serviceAccountName override is not allowed", fieldPath))
	}
	// DeprecatedServiceAccount is the pre-1.8 alias for ServiceAccountName.
	// Kubernetes still honors it for backward compatibility so a tenant could
	// otherwise bypass the ServiceAccountName check by setting this field.
	if ps.DeprecatedServiceAccount != "" {
		errs = errors.Join(errs, fmt.Errorf("%s.serviceAccount (deprecated, alias for serviceAccountName) override is not allowed", fieldPath))
	}
	for i, v := range ps.Volumes {
		if v.HostPath != nil {
			errs = errors.Join(errs, fmt.Errorf("%s.volumes[%d].hostPath (%q) is not allowed", fieldPath, i, v.Name))
		}
	}

	for i := range ps.Containers {
		group := fmt.Sprintf("%s.containers[%s]", fieldPath, ps.Containers[i].Name)
		errs = errors.Join(errs, ValidateContainerSafety(group, &ps.Containers[i]))
	}
	for i := range ps.InitContainers {
		group := fmt.Sprintf("%s.initContainers[%s]", fieldPath, ps.InitContainers[i].Name)
		errs = errors.Join(errs, ValidateContainerSafety(group, &ps.InitContainers[i]))
	}

	return errs
}

// ValidateContainerSafety rejects the SecurityContext fields of a single
// container that would let a low-privilege tenant escape the container
// sandbox: privileged=true, allowPrivilegeEscalation=true, and dangerous
// Linux capabilities.
//
// It exists as a standalone check because the Environment CRD exposes
// `spec.runtime.container` and `spec.builder.container` — a bare
// *apiv1.Container that is merged into the runtime/builder pod but is
// NOT part of any PodSpec, so ValidatePodSpecSafety never reaches it.
// Leaving the Container SecurityContext unchecked is a bypass of the
// PodSpec hardening (GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 /
// GHSA-v455-mv2v-5g92) — closes GHSA-m63v-2g9w-2w6v.
//
// ValidatePodSpecSafety calls this for each (init)container, and
// Environment.Validate calls it directly for Runtime.Container /
// Builder.Container. A nil container is accepted (the field is optional).
//
// The fieldPath argument is used as a prefix in error messages so the
// caller can identify which container failed (e.g.
// "Environment.spec.runtime.container").
func ValidateContainerSafety(fieldPath string, c *apiv1.Container) error {
	if c == nil || c.SecurityContext == nil {
		return nil
	}
	var errs error
	sc := c.SecurityContext
	if sc.Privileged != nil && *sc.Privileged {
		errs = errors.Join(errs, fmt.Errorf(
			"%s.securityContext.privileged=true is not allowed", fieldPath))
	}
	if sc.AllowPrivilegeEscalation != nil && *sc.AllowPrivilegeEscalation {
		errs = errors.Join(errs, fmt.Errorf(
			"%s.securityContext.allowPrivilegeEscalation=true is not allowed", fieldPath))
	}
	if sc.Capabilities != nil {
		for _, cap := range sc.Capabilities.Add {
			if _, ok := allowedCapabilities[cap]; !ok {
				errs = errors.Join(errs, fmt.Errorf(
					"%s.securityContext.capabilities.add[%q] is not in the allowlist (only NET_BIND_SERVICE may be added)",
					fieldPath, cap))
			}
		}
	}
	return errs
}
