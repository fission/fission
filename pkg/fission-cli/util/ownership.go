// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

// GetOwnedFunctionAlias fetches the named FunctionAlias and confirms it
// belongs to fnName. Shared RFC-0025 preflight: a typo'd --alias/
// --function-alias should surface here as a clear error rather than an
// opaque downstream failure (a router 404 for `fn test`, an empty pod list
// for `fn pods`/`fn logs`, a route silently pinned to the wrong function's
// alias for `route create`/`route update`, a bare `fn rollback` repointing
// the wrong function's alias).
//
// Lives here rather than exported from pkg/fission-cli/cmd/function because
// pkg/fission-cli/cmd/httptrigger already imports that package
// (create.go/test.go); exporting it there for httptrigger to call back would
// cycle.
func GetOwnedFunctionAlias(ctx context.Context, client cmd.Client, namespace, fnName, aliasName string) (*fv1.FunctionAlias, error) {
	alias, err := client.FissionClientSet.CoreV1().FunctionAliases(namespace).Get(ctx, aliasName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("alias %q not found for function %q: %w", aliasName, fnName, err)
	}
	if alias.Spec.FunctionName != fnName {
		return nil, fmt.Errorf("alias %q targets function %q, not %q", aliasName, alias.Spec.FunctionName, fnName)
	}
	return alias, nil
}

// GetOwnedFunctionVersion fetches the named FunctionVersion and confirms it
// belongs to fnName. See GetOwnedFunctionAlias.
func GetOwnedFunctionVersion(ctx context.Context, client cmd.Client, namespace, fnName, versionName string) (*fv1.FunctionVersion, error) {
	version, err := client.FissionClientSet.CoreV1().FunctionVersions(namespace).Get(ctx, versionName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("version %q not found for function %q: %w", versionName, fnName, err)
	}
	if version.Spec.FunctionName != fnName {
		return nil, fmt.Errorf("version %q targets function %q, not %q", versionName, version.Spec.FunctionName, fnName)
	}
	return version, nil
}

// MutuallyExclusive returns the standard "--name1 and --name2 are mutually
// exclusive" error when both val1 and val2 are non-empty (nil otherwise).
// Shared by every RFC-0025 alias/version flag pair (`fn test`'s --alias/
// --version, `fn pods`/`fn logs`'s same, `route create`/`route update`'s
// --function-alias/--function-version) so the guard and its wording only
// need to be written once; callers pass their own flag NAMES so the message
// still names the flags as the user typed them.
func MutuallyExclusive(name1, val1, name2, val2 string) error {
	if val1 != "" && val2 != "" {
		return fmt.Errorf("--%s and --%s are mutually exclusive", name1, name2)
	}
	return nil
}
