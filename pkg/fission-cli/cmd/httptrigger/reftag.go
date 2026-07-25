// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cmd"
)

// validateFnRefTagFlags enforces the RFC-0025 --function-alias/
// --function-version syntactic constraints, shared by `route create` and
// `route update`: the two flags are mutually exclusive, and either one is
// valid only when paired with exactly one --function -- a weighted
// multi-function reference (canary, --function supplied twice with
// --weight) has no single function to pin a tag against.
func validateFnRefTagFlags(alias, version string, functionList []string) error {
	if alias != "" && version != "" {
		return errors.New("--function-alias and --function-version are mutually exclusive")
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
// API server, mirroring `fn test`'s resolveTestRefSuffix
// (pkg/fission-cli/cmd/function/test.go): the named FunctionAlias/
// FunctionVersion must exist and its Spec.FunctionName must equal fnName, so
// a typo or a tag borrowed from a different function's lineage surfaces here
// as a clear error instead of an opaque router 404 once traffic hits the
// route. A no-op when neither alias nor version is set.
func checkFnRefTagOwnership(ctx context.Context, client cmd.Client, namespace, fnName, alias, version string) error {
	switch {
	case alias != "":
		a, err := client.FissionClientSet.CoreV1().FunctionAliases(namespace).Get(ctx, alias, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("alias %q not found for function %q: %w", alias, fnName, err)
		}
		if a.Spec.FunctionName != fnName {
			return fmt.Errorf("alias %q targets function %q, not %q", alias, a.Spec.FunctionName, fnName)
		}
	case version != "":
		v, err := client.FissionClientSet.CoreV1().FunctionVersions(namespace).Get(ctx, version, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("version %q not found for function %q: %w", version, fnName, err)
		}
		if v.Spec.FunctionName != fnName {
			return fmt.Errorf("version %q targets function %q, not %q", version, v.Spec.FunctionName, fnName)
		}
	}
	return nil
}
