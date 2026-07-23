// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"fmt"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	name      string
	namespace string
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) error {
	_, ns, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error updating function alias: %w", err)
	}
	opts.name = input.String(flagkey.AliasName)
	opts.namespace = ns
	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	aliases := opts.Client().FissionClientSet.CoreV1().FunctionAliases(opts.namespace)
	updated, err := util.UpdateOnConflict(input.Context(), aliases, opts.name, mutateAliasSpec(input))
	if err != nil {
		return fmt.Errorf("error updating function alias: %w", err)
	}

	fmt.Printf("function alias '%v' updated\n", opts.name)

	if input.Bool(flagkey.AliasWait) {
		timeout := input.Duration(flagkey.WaitTimeout)
		// updated.Spec.Version is empty for a PackageDigest-pinned alias;
		// waitForResolved treats that as "just wait for Resolved=True" (see
		// its doc comment) since the caller cannot name the target version
		// the async digest resolution will land on.
		if err := WaitForResolved(input.Context(), opts.Client().FissionClientSet, opts.namespace, opts.name, updated.Spec.Version, timeout); err != nil {
			return fmt.Errorf("error waiting for function alias to resolve: %w", err)
		}
		fmt.Printf("function alias '%v' resolved\n", opts.name)
	}

	return nil
}

// mutateAliasSpec builds the IsSet-gated mutation applied to the freshly
// fetched FunctionAlias by util.UpdateOnConflict: only flags the user
// actually passed change the resource, so an update that never mentions a
// field leaves it intact. --version and --package-digest are mutually
// exclusive pin styles (webhook-enforced XOR) — setting one clears the other
// so a lone `alias update --version v2` moves a digest-pinned alias back to
// name-pinning instead of tripping the XOR rule. --clear-weight is applied
// last so it wins over a --weight/--secondary-version passed in the same
// call, matching its "drop the split" intent.
func mutateAliasSpec(input cli.Input) func(*fv1.FunctionAlias) {
	return func(cur *fv1.FunctionAlias) {
		if input.IsSet(flagkey.AliasVersion) {
			cur.Spec.Version = input.String(flagkey.AliasVersion)
			cur.Spec.PackageDigest = ""
		}
		if input.IsSet(flagkey.AliasPackageDigest) {
			cur.Spec.PackageDigest = input.String(flagkey.AliasPackageDigest)
			cur.Spec.Version = ""
		}
		if input.IsSet(flagkey.AliasWeight) {
			w := input.Int(flagkey.AliasWeight)
			cur.Spec.Weight = &w
		}
		if input.IsSet(flagkey.AliasSecondaryVersion) {
			cur.Spec.SecondaryVersion = input.String(flagkey.AliasSecondaryVersion)
		}
		if input.Bool(flagkey.AliasClearWeight) {
			cur.Spec.Weight = nil
			cur.Spec.SecondaryVersion = ""
		}
	}
}
