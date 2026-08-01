// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

// EnvSecretNames returns the Secret names this function references through
// Env[].valueFrom.secretKeyRef and EnvFrom[].secretRef. These references are
// same-namespace by construction (LocalObjectReference carries no namespace).
// The executor's rotation watcher and the pod-template RVSum must treat them
// exactly like Spec.Secrets: Kubernetes never refreshes env in a running
// container, so a rotation that misses them propagates to nothing — silently
// stale credentials (RFC-0030 §5).
func (spec FunctionSpec) EnvSecretNames() []string {
	var names []string
	for _, e := range spec.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			names = append(names, e.ValueFrom.SecretKeyRef.Name)
		}
	}
	for _, src := range spec.EnvFrom {
		if src.SecretRef != nil {
			names = append(names, src.SecretRef.Name)
		}
	}
	return names
}

// EnvConfigMapNames is the ConfigMap counterpart of EnvSecretNames.
func (spec FunctionSpec) EnvConfigMapNames() []string {
	var names []string
	for _, e := range spec.Env {
		if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
			names = append(names, e.ValueFrom.ConfigMapKeyRef.Name)
		}
	}
	for _, src := range spec.EnvFrom {
		if src.ConfigMapRef != nil {
			names = append(names, src.ConfigMapRef.Name)
		}
	}
	return names
}
