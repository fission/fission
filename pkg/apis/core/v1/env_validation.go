// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"errors"
	"strings"

	apiv1 "k8s.io/api/core/v1"
)

// Reserved environment-variable names (RFC-0030 §1). Two tiers, two reasons:
//
//   - Platform contract: FISSION_* and RESOURCE_VERSION_COUNT are the names the
//     platform itself sets on the user container. The load-bearing case is
//     FISSION_STATE_URL — redirecting it exfiltrates the RFC-0023 state token
//     to an attacker-chosen endpoint.
//   - Interpreter/proxy hijack: the names that turn env injection into a
//     code-execution or traffic-interception primitive (LD_PRELOAD,
//     NODE_OPTIONS, PYTHONPATH, the proxy set). A function legitimately
//     needing a proxy sets it in its own code or environment image.
//
// OTEL_* is deliberately NOT reserved: OTel env lands on the fetcher sidecar
// only, so there is no platform value on the user container to protect.
//
// Admission checks literal Env names against this list as fast feedback; the
// authoritative enforcement is at injection time (kubelet last-wins ordering
// on newdeploy/container, fetcher-side key filtering on poolmgr), because
// EnvFrom-expanded names are the referenced object's data keys — unknowable
// and mutable after admission.
const ReservedEnvPrefix = "FISSION_"

var reservedEnvNames = map[string]struct{}{
	"RESOURCE_VERSION_COUNT": {},
	"HTTP_PROXY":             {},
	"HTTPS_PROXY":            {},
	"NO_PROXY":               {},
	"http_proxy":             {},
	"https_proxy":            {},
	"no_proxy":               {},
	"LD_PRELOAD":             {},
	"NODE_OPTIONS":           {},
	"PYTHONPATH":             {},
}

// IsReservedEnvName reports whether name is platform-reserved and must never
// be user-settable on the function container. Exported because the poolmgr
// phase's injection-time filter (fetcher-side EnvFrom expansion) enforces the
// same list.
func IsReservedEnvName(name string) bool {
	if strings.HasPrefix(name, ReservedEnvPrefix) {
		return true
	}
	_, reserved := reservedEnvNames[name]
	return reserved
}

// validateFunctionEnv validates FunctionSpec.Env / EnvFrom (RFC-0030 §1):
// literal names non-empty, unique and non-reserved; valueFrom narrowed to
// secretKeyRef/configMapKeyRef (poolmgr's specialize-time injection cannot
// honor pod-level fieldRef/resourceFieldRef portably); each EnvFrom source
// names exactly one of a Secret or a ConfigMap.
func validateFunctionEnv(env []apiv1.EnvVar, envFrom []apiv1.EnvFromSource) error {
	var errs error
	seen := make(map[string]struct{}, len(env))
	for _, e := range env {
		if e.Name == "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "environment variable name must not be empty"))
			continue
		}
		if _, dup := seen[e.Name]; dup {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "duplicate environment variable name"))
		}
		seen[e.Name] = struct{}{}
		if IsReservedEnvName(e.Name) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "name is reserved by the platform (FISSION_*, RESOURCE_VERSION_COUNT, proxy and interpreter-hijack variables)"))
		}
		if e.ValueFrom != nil {
			if e.Value != "" {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "value and valueFrom are mutually exclusive"))
			}
			if e.ValueFrom.FieldRef != nil || e.ValueFrom.ResourceFieldRef != nil {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "valueFrom supports only secretKeyRef and configMapKeyRef (downward-API refs cannot be honored portably across executors)"))
			}
			refs := 0
			// An empty object name passes the API server's schema (name is
			// optional on LocalObjectReference) but makes the built pod spec
			// unadmittable, leaving the function healthy-looking with no pods.
			if ref := e.ValueFrom.SecretKeyRef; ref != nil {
				refs++
				if ref.Name == "" {
					errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "secretKeyRef.name must not be empty"))
				}
			}
			if ref := e.ValueFrom.ConfigMapKeyRef; ref != nil {
				refs++
				if ref.Name == "" {
					errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "configMapKeyRef.name must not be empty"))
				}
			}
			if refs != 1 && e.ValueFrom.FieldRef == nil && e.ValueFrom.ResourceFieldRef == nil {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.Env", e.Name, "valueFrom must name exactly one of secretKeyRef or configMapKeyRef"))
			}
		}
	}
	for i, src := range envFrom {
		refs := 0
		if ref := src.SecretRef; ref != nil {
			refs++
			if ref.Name == "" {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.EnvFrom", i, "secretRef.name must not be empty"))
			}
		}
		if ref := src.ConfigMapRef; ref != nil {
			refs++
			if ref.Name == "" {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.EnvFrom", i, "configMapRef.name must not be empty"))
			}
		}
		if refs != 1 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "FunctionSpec.EnvFrom", i, "each envFrom source must name exactly one of secretRef or configMapRef"))
		}
	}
	return errs
}
