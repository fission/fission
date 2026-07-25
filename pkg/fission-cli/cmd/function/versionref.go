// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

// getOwnedFunctionAlias fetches the named FunctionAlias and confirms it
// belongs to fnName. Shared RFC-0025 preflight: a typo'd --alias should
// surface here as a clear error rather than an opaque downstream failure
// (a router 404 for `fn test`, an empty pod list for `fn pods`/`fn logs`).
func getOwnedFunctionAlias(ctx context.Context, client cmd.Client, namespace, fnName, aliasName string) (*v1.FunctionAlias, error) {
	alias, err := client.FissionClientSet.CoreV1().FunctionAliases(namespace).Get(ctx, aliasName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("alias %q not found for function %q: %w", aliasName, fnName, err)
	}
	if alias.Spec.FunctionName != fnName {
		return nil, fmt.Errorf("alias %q targets function %q, not %q", aliasName, alias.Spec.FunctionName, fnName)
	}
	return alias, nil
}

// getOwnedFunctionVersion fetches the named FunctionVersion and confirms it
// belongs to fnName. See getOwnedFunctionAlias.
func getOwnedFunctionVersion(ctx context.Context, client cmd.Client, namespace, fnName, versionName string) (*v1.FunctionVersion, error) {
	version, err := client.FissionClientSet.CoreV1().FunctionVersions(namespace).Get(ctx, versionName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("version %q not found for function %q: %w", versionName, fnName, err)
	}
	if version.Spec.FunctionName != fnName {
		return nil, fmt.Errorf("version %q targets function %q, not %q", versionName, version.Spec.FunctionName, fnName)
	}
	return version, nil
}

// resolveVersionLabelFilter resolves the RFC-0025 --alias/--version flags
// (mutually exclusive) to the FunctionVersion name that `fn pods`/`fn logs`
// should filter the fission.io/function-version pod label on. Returns "" when
// neither flag is set, meaning no version filter should be applied.
//
// Unlike `fn test`'s resolveTestRefSuffix -- which hands the alias/version
// name straight to the router as a route suffix and lets the router resolve
// it -- pods/logs filter a Kubernetes label selector directly, so --alias
// must be resolved here to its *effective* target: Spec.Version when the
// alias is name-pinned, else Status.ResolvedVersion for a PackageDigest-pinned
// alias (both preflight-checked via getOwnedFunctionAlias/getOwnedFunctionVersion,
// same as `fn test`).
func resolveVersionLabelFilter(ctx context.Context, client cmd.Client, namespace, fnName, aliasName, versionName string) (string, error) {
	if aliasName != "" && versionName != "" {
		return "", errors.New("--alias and --version are mutually exclusive")
	}

	switch {
	case aliasName != "":
		alias, err := getOwnedFunctionAlias(ctx, client, namespace, fnName, aliasName)
		if err != nil {
			return "", err
		}
		target := alias.Spec.Version
		if target == "" {
			target = alias.Status.ResolvedVersion
		}
		if target == "" {
			return "", fmt.Errorf("alias %q has not resolved to a version yet", aliasName)
		}
		return target, nil
	case versionName != "":
		if _, err := getOwnedFunctionVersion(ctx, client, namespace, fnName, versionName); err != nil {
			return "", err
		}
		return versionName, nil
	default:
		return "", nil
	}
}
