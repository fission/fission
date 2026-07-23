// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/functionalias"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type RollbackSubCommand struct {
	cmd.CommandActioner
	fnName    string
	aliasName string
	namespace string
	target    string
	detach    bool
	oldTarget string
}

// Rollback repoints a FunctionAlias at a previously resolved FunctionVersion
// (RFC-0025 Phase 3): by default the alias's Status.History's last entry
// (the most recently superseded target, kept for exactly this purpose), or an
// explicit --to version. It is always a full repoint — Weight and
// SecondaryVersion are cleared, so a rollback issued mid-canary stops the
// traffic split rather than rolling back only the primary target.
func Rollback(input cli.Input) error {
	return (&RollbackSubCommand{}).do(input)
}

func (opts *RollbackSubCommand) do(input cli.Input) error {
	if err := opts.complete(input); err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *RollbackSubCommand) complete(input cli.Input) error {
	opts.fnName = input.String(flagkey.FnName)
	opts.aliasName = input.String(flagkey.FnRollbackAlias)
	opts.detach = input.Bool(flagkey.FnRollbackDetach)

	_, ns, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error rolling back function: %w", err)
	}
	opts.namespace = ns

	alias, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(ns).Get(input.Context(), opts.aliasName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error reading function alias '%v': %w", opts.aliasName, err)
	}

	// Preflight: the alias must actually belong to the function the caller
	// named — --name/--alias are independent flags, so a typo'd --alias
	// pointing at a different function's alias must not silently repoint it.
	if alias.Spec.FunctionName != opts.fnName {
		return fmt.Errorf("function alias '%v' targets function '%v', not '%v'", opts.aliasName, alias.Spec.FunctionName, opts.fnName)
	}

	if input.IsSet(flagkey.FnRollbackTo) {
		opts.target = input.String(flagkey.FnRollbackTo)
	} else {
		hist := alias.Status.History
		if len(hist) == 0 {
			return errors.New("no previous target recorded: function alias has no Status.History and --to was not given")
		}
		opts.target = hist[len(hist)-1].Version
	}
	if opts.target == "" {
		return errors.New("no previous target recorded: the last Status.History entry has an empty version")
	}

	// SPEC-MANAGED GUARD: an alias carrying the `fission spec` deployment-UID
	// annotation is owned by a Git-tracked manifest; `spec apply` reconciles
	// it back to spec.version on its next run, silently undoing a bare
	// rollback. Refuse unless --detach, which strips both deployment
	// annotations (name+UID) in the same update that performs the repoint —
	// see run().
	if _, managed := alias.Annotations[spec.FISSION_DEPLOYMENT_UID_KEY]; managed && !opts.detach {
		return fmt.Errorf("function alias '%v' is managed by `fission spec` (Git); the next spec apply will revert this rollback. "+
			"Re-run with --detach to strip spec ownership, and update your Git repo: set spec.version: %v in the FunctionAlias manifest",
			opts.aliasName, opts.target)
	}

	opts.oldTarget = aliasTargetDisplay(alias.Spec)

	return nil
}

func (opts *RollbackSubCommand) run(input cli.Input) error {
	opts.warnEnvDrift(input)

	aliases := opts.Client().FissionClientSet.CoreV1().FunctionAliases(opts.namespace)

	_, err := util.UpdateOnConflict(input.Context(), aliases, opts.aliasName, func(cur *fv1.FunctionAlias) {
		// Full repoint: name-pin wins over any previous digest pin (a digest
		// pin would fight the resolver over which version is "current"), and
		// Weight/SecondaryVersion are cleared so a rollback mid-canary stops
		// the traffic split instead of rolling back only the primary target.
		cur.Spec.Version = opts.target
		cur.Spec.PackageDigest = ""
		cur.Spec.Weight = nil
		cur.Spec.SecondaryVersion = ""

		if opts.detach {
			delete(cur.Annotations, spec.FISSION_DEPLOYMENT_UID_KEY)
			delete(cur.Annotations, spec.FISSION_DEPLOYMENT_NAME_KEY)
		}
	})
	if err != nil {
		return fmt.Errorf("error rolling back function alias '%v': %w", opts.aliasName, err)
	}

	fmt.Printf("function alias '%v' rolled back: %v -> %v\n", opts.aliasName, opts.oldTarget, opts.target)

	if input.Bool(flagkey.FnRollbackWait) {
		timeout := input.Duration(flagkey.WaitTimeout)
		if err := functionalias.WaitForResolved(input.Context(), opts.Client().FissionClientSet, opts.namespace, opts.aliasName, opts.target, timeout); err != nil {
			return fmt.Errorf("error waiting for function alias to resolve: %w", err)
		}
		fmt.Printf("function alias '%v' resolved\n", opts.aliasName)
	}

	return nil
}

// warnEnvDrift prints a non-blocking WARNING when opts.target was published
// under an Environment generation the live Environment has since moved past
// (RFC-0025 "Environment & Package changes across the version boundary": an
// env update bumps no Function Generation and recycles pods under EVERY
// version, so it sits outside the version boundary entirely). "Instant
// rollback" here means the FunctionSpec snapshot — code/config — not the
// runtime image, and this warning is the CLI-side surfacing of that gap;
// see the AliasReconciler's EnvDrift condition for the same check running
// continuously in-cluster. A missing target FunctionVersion or a
// missing/unreadable Environment is not assessable and is silently
// skipped — never blocks or errors the rollback, mirroring the
// reconciler's "absence means not assessable" EnvDrift contract.
func (opts *RollbackSubCommand) warnEnvDrift(input cli.Input) {
	v, err := opts.Client().FissionClientSet.CoreV1().FunctionVersions(opts.namespace).Get(input.Context(), opts.target, metav1.GetOptions{})
	if err != nil {
		return
	}

	// Mirrors publish.go:118's envNS fallback: an unset Snapshot Environment
	// namespace means "same namespace as the function", and opts.namespace
	// is that namespace (FunctionVersions/FunctionAliases live alongside
	// their Function).
	envNS := v.Spec.Snapshot.Environment.Namespace
	if envNS == "" {
		envNS = opts.namespace
	}
	env, err := opts.Client().FissionClientSet.CoreV1().Environments(envNS).Get(input.Context(), v.Spec.Snapshot.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return
	}

	if v.Spec.EnvObservedGeneration != env.Generation {
		fmt.Printf("WARNING: target version %v was published under env %v/%v generation %v; live env is generation %v — rollback restores code/config, not the runtime image\n",
			opts.target, envNS, env.Name, v.Spec.EnvObservedGeneration, env.Generation)
	}
}

// aliasTargetDisplay renders a FunctionAliasSpec's target for the "old ->
// new" rollback summary, mirroring the XOR of Version/PackageDigest.
func aliasTargetDisplay(s fv1.FunctionAliasSpec) string {
	if s.Version != "" {
		return s.Version
	}
	if s.PackageDigest != "" {
		return s.PackageDigest
	}
	return util.NoneValue
}
