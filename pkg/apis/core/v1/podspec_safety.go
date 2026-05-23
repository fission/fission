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

package v1

import (
	"errors"
	"fmt"

	apiv1 "k8s.io/api/core/v1"
)

// dangerousCapabilities lists Linux capabilities that effectively grant root
// or break the container sandbox. Tenants that can write to Environment or
// Function PodSpec must not be able to add these via securityContext.
var dangerousCapabilities = map[apiv1.Capability]struct{}{
	"SYS_ADMIN":       {},
	"NET_ADMIN":       {},
	"SYS_PTRACE":      {},
	"SYS_MODULE":      {},
	"DAC_READ_SEARCH": {},
	"DAC_OVERRIDE":    {},
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

	checkContainer := func(group string, c apiv1.Container) error {
		var cerrs error
		sc := c.SecurityContext
		if sc == nil {
			return nil
		}
		if sc.Privileged != nil && *sc.Privileged {
			cerrs = errors.Join(cerrs, fmt.Errorf(
				"%s.%s[%s].securityContext.privileged=true is not allowed", fieldPath, group, c.Name))
		}
		if sc.AllowPrivilegeEscalation != nil && *sc.AllowPrivilegeEscalation {
			cerrs = errors.Join(cerrs, fmt.Errorf(
				"%s.%s[%s].securityContext.allowPrivilegeEscalation=true is not allowed", fieldPath, group, c.Name))
		}
		if sc.Capabilities != nil {
			for _, cap := range sc.Capabilities.Add {
				if _, bad := dangerousCapabilities[cap]; bad {
					cerrs = errors.Join(cerrs, fmt.Errorf(
						"%s.%s[%s].securityContext.capabilities.add[%q] is not allowed", fieldPath, group, c.Name, cap))
				}
			}
		}
		return cerrs
	}

	for _, c := range ps.Containers {
		errs = errors.Join(errs, checkContainer("containers", c))
	}
	for _, c := range ps.InitContainers {
		errs = errors.Join(errs, checkContainer("initContainers", c))
	}

	return errs
}
