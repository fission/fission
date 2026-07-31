// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"
	"strings"

	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// parseFunctionEnvFlags turns the RFC-0030 CLI surface into FunctionSpec
// fields:
//
//	--env-var KEY=VALUE          -> Env literal
//	--env-from-secret name       -> EnvFrom secretRef (whole object)
//	--env-from-secret name/key   -> Env valueFrom secretKeyRef, variable named after the key
//	--env-from-secret name/k:ENV -> Env valueFrom secretKeyRef, variable named ENV
//
// (--env-from-configmap mirrors the secret forms.) Key-level references
// deliberately become Env entries rather than EnvFrom sources: valueFrom is
// the Kubernetes construct for single-key selection, and it keeps the merge
// precedence (EnvFrom < Env) matching user intent — a named single key beats
// a whole-object projection.
func parseFunctionEnvFlags(input cli.Input) ([]apiv1.EnvVar, []apiv1.EnvFromSource, error) {
	var env []apiv1.EnvVar
	var envFrom []apiv1.EnvFromSource

	for _, kv := range input.StringSlice(flagkey.FnEnvVar) {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("--%s %q must be KEY=VALUE", flagkey.FnEnvVar, kv)
		}
		env = append(env, apiv1.EnvVar{Name: name, Value: value})
	}

	for _, ref := range input.StringSlice(flagkey.FnEnvFromSecret) {
		objName, key, envName, err := splitEnvSourceRef(flagkey.FnEnvFromSecret, ref)
		if err != nil {
			return nil, nil, err
		}
		if key == "" {
			envFrom = append(envFrom, apiv1.EnvFromSource{
				SecretRef: &apiv1.SecretEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: objName}},
			})
			continue
		}
		env = append(env, apiv1.EnvVar{
			Name: envName,
			ValueFrom: &apiv1.EnvVarSource{SecretKeyRef: &apiv1.SecretKeySelector{
				LocalObjectReference: apiv1.LocalObjectReference{Name: objName},
				Key:                  key,
			}},
		})
	}

	for _, ref := range input.StringSlice(flagkey.FnEnvFromConfigMap) {
		objName, key, envName, err := splitEnvSourceRef(flagkey.FnEnvFromConfigMap, ref)
		if err != nil {
			return nil, nil, err
		}
		if key == "" {
			envFrom = append(envFrom, apiv1.EnvFromSource{
				ConfigMapRef: &apiv1.ConfigMapEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: objName}},
			})
			continue
		}
		env = append(env, apiv1.EnvVar{
			Name: envName,
			ValueFrom: &apiv1.EnvVarSource{ConfigMapKeyRef: &apiv1.ConfigMapKeySelector{
				LocalObjectReference: apiv1.LocalObjectReference{Name: objName},
				Key:                  key,
			}},
		})
	}

	return env, envFrom, nil
}

// splitEnvSourceRef parses name[/key][:ENV]. The :ENV rename is only
// meaningful with a key — a whole-object projection has no single variable
// to rename — so 'name:ENV' without a key is rejected rather than guessed at.
func splitEnvSourceRef(flagName, ref string) (objName, key, envName string, err error) {
	objName, rest, hasKey := strings.Cut(ref, "/")
	if objName == "" {
		return "", "", "", fmt.Errorf("--%s %q must be name[/key][:ENV]", flagName, ref)
	}
	if !hasKey {
		if strings.Contains(objName, ":") {
			return "", "", "", fmt.Errorf("--%s %q renames a single key (:ENV), so it needs a /key part", flagName, ref)
		}
		return objName, "", "", nil
	}
	key, envName, hasRename := strings.Cut(rest, ":")
	if key == "" {
		return "", "", "", fmt.Errorf("--%s %q must be name[/key][:ENV]", flagName, ref)
	}
	if !hasRename {
		envName = key
	} else if envName == "" {
		return "", "", "", fmt.Errorf("--%s %q has an empty :ENV rename", flagName, ref)
	}
	return objName, key, envName, nil
}

// applyFunctionEnvFlags applies the three RFC-0030 env flags to fn, replacing
// Spec.Env and Spec.EnvFrom together when any of them is passed. They have to
// move as a unit because a key-level --env-from-* reference becomes an Env
// entry, so the flags feed both fields jointly; passing only some of them
// therefore drops what the others contributed, which warns rather than
// happening in silence. Shared by fn update and fn update-container.
func applyFunctionEnvFlags(input cli.Input, fn *fv1.Function) error {
	envFlags := []string{flagkey.FnEnvVar, flagkey.FnEnvFromSecret, flagkey.FnEnvFromConfigMap}
	set := make([]string, 0, len(envFlags))
	for _, f := range envFlags {
		if input.IsSet(f) {
			set = append(set, f)
		}
	}
	if len(set) == 0 {
		return nil
	}
	if (len(fn.Spec.Env) > 0 || len(fn.Spec.EnvFrom) > 0) && len(set) < len(envFlags) {
		console.Warn(fmt.Sprintf("--%s replace the function's entire environment: variables set through the flags you did not pass will be removed. Pass every env flag the function needs in one update.",
			strings.Join(set, "/--")))
	}
	env, envFrom, err := parseFunctionEnvFlags(input)
	if err != nil {
		return err
	}
	fn.Spec.Env = env
	fn.Spec.EnvFrom = envFrom
	return nil
}
