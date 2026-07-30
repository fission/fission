// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// resolveVersionLabelFilter resolves the RFC-0025 --alias/--version flags
// (mutually exclusive) to the FunctionVersion name that `fn pods`/`fn logs`
// should filter the fission.io/function-version pod label on. Returns "" when
// neither flag is set, meaning no version filter should be applied.
//
// Unlike `fn test`'s resolveTestRefSuffix -- which hands the alias/version
// name straight to the router as a route suffix and lets the router resolve
// it -- pods/logs filter a Kubernetes label selector directly, so --alias
// must be resolved here to its *effective* target (fv1.FunctionAlias's
// EffectiveTarget: Spec.Version when the alias is name-pinned, else
// Status.ResolvedVersion for a PackageDigest-pinned alias), both
// preflight-checked via util.GetOwnedFunctionAlias/util.GetOwnedFunctionVersion,
// same as `fn test`.
func resolveVersionLabelFilter(ctx context.Context, client cmd.Client, namespace, fnName, aliasName, versionName string) (string, error) {
	if err := util.MutuallyExclusive(flagkey.FnTestAlias, aliasName, flagkey.FnTestVersion, versionName); err != nil {
		return "", err
	}

	switch {
	case aliasName != "":
		alias, err := util.GetOwnedFunctionAlias(ctx, client, namespace, fnName, aliasName)
		if err != nil {
			return "", err
		}
		target := alias.EffectiveTarget()
		if target == "" {
			return "", fmt.Errorf("alias %q has not resolved to a version yet", aliasName)
		}
		return target, nil
	case versionName != "":
		if _, err := util.GetOwnedFunctionVersion(ctx, client, namespace, fnName, versionName); err != nil {
			return "", err
		}
		return versionName, nil
	default:
		return "", nil
	}
}
