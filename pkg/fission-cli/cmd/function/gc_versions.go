// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/versioning"
)

type GCVersionsSubCommand struct {
	cmd.CommandActioner
	fnName    string
	namespace string
	retain    int
}

// GCVersions runs one on-demand retention-GC sweep of a function's
// FunctionVersions via versioning.SweepVersions (pkg/versioning/
// retentiongc.go) -- the exact sweep core the buildermgr-hosted
// RetentionGCReconciler runs automatically on every opted-in Function. Useful
// to force a sweep without waiting for the next triggering event, or to
// preview the effect of a lower --keep before persisting it to the
// function's Spec.Versioning.Retain. Never deletes an alias-referenced
// version (invariant V3) or the newest/only version, however low --keep is
// set.
func GCVersions(input cli.Input) error {
	return (&GCVersionsSubCommand{}).do(input)
}

func (opts *GCVersionsSubCommand) do(input cli.Input) error {
	if err := opts.complete(input); err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *GCVersionsSubCommand) complete(input cli.Input) error {
	opts.fnName = input.String(flagkey.FnName)

	_, ns, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error sweeping function versions: %w", err)
	}
	opts.namespace = ns

	if input.IsSet(flagkey.GCVersionsKeep) {
		opts.retain = input.Int(flagkey.GCVersionsKeep)
		if opts.retain < 1 {
			return fmt.Errorf("--%s must be at least 1", flagkey.GCVersionsKeep)
		}
		return nil
	}

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(ns).Get(input.Context(), opts.fnName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read function '%v': %w", opts.fnName, err)
	}
	opts.retain = versioning.DefaultRetain
	if fn.Spec.Versioning != nil && fn.Spec.Versioning.Retain != nil {
		opts.retain = *fn.Spec.Versioning.Retain
	}
	return nil
}

func (opts *GCVersionsSubCommand) run(input cli.Input) error {
	result, err := versioning.SweepVersions(input.Context(), opts.Client().FissionClientSet, opts.namespace, opts.fnName, opts.retain)
	if err != nil {
		return fmt.Errorf("error sweeping function versions: %w", err)
	}

	skipped := len(result.SkippedReferenced) + len(result.SkippedForbidden)
	fmt.Printf("deleted %d, skipped %d, retained %d\n", len(result.Deleted), skipped, len(result.Retained))
	if len(result.SkippedForbidden) > 0 {
		fmt.Printf("%d delete(s) denied (forbidden); a version-alias admission race, retry later\n", len(result.SkippedForbidden))
	}
	return nil
}
