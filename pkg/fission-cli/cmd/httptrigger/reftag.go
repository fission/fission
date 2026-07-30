// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"context"
	"fmt"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// validateFnRefTagFlags enforces the RFC-0025 --function-alias/
// --function-version syntactic constraints, shared by `route create` and
// `route update`: the two flags are mutually exclusive, and either one is
// valid only when paired with exactly one --function -- a weighted
// multi-function reference (canary, --function supplied twice with
// --weight) has no single function to pin a tag against.
func validateFnRefTagFlags(alias, version string, functionList []string) error {
	if err := util.MutuallyExclusive(flagkey.HtFnAlias, alias, flagkey.HtFnVersion, version); err != nil {
		return err
	}
	if alias == "" && version == "" {
		return nil
	}
	if len(functionList) != 1 {
		return fmt.Errorf("--function-alias/--function-version require exactly one --function, got %d "+
			"(weighted multi-function routing cannot be combined with a tag pin)", len(functionList))
	}
	return nil
}

// checkFnRefTagOwnership preflight-checks an alias/version tag against the
// API server, via the shared RFC-0025 ownership preflight
// (util.GetOwnedFunctionAlias/util.GetOwnedFunctionVersion -- `fn test`'s
// resolveTestRefSuffix and `fn pods`/`fn logs`'s resolveVersionLabelFilter
// use the same helpers): the named FunctionAlias/FunctionVersion must exist
// and its Spec.FunctionName must equal fnName, so a typo or a tag borrowed
// from a different function's lineage surfaces here as a clear error
// instead of an opaque router 404 once traffic hits the route. A no-op when
// neither alias nor version is set.
func checkFnRefTagOwnership(ctx context.Context, client cmd.Client, namespace, fnName, alias, version string) error {
	switch {
	case alias != "":
		_, err := util.GetOwnedFunctionAlias(ctx, client, namespace, fnName, alias)
		return err
	case version != "":
		_, err := util.GetOwnedFunctionVersion(ctx, client, namespace, fnName, version)
		return err
	}
	return nil
}
