// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	alias *fv1.FunctionAlias
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) (err error) {
	name := input.String(flagkey.AliasName)
	fnName := input.String(flagkey.AliasFunction)

	_, ns, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in creating function alias: %w", err)
	}

	// Preflight: the function must exist in the same namespace, and its UID
	// is what the alias's ownerRef pins (mirrors versioning.Publish's
	// FunctionVersion ownerRef — see pkg/versioning/publish.go).
	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(ns).Get(input.Context(), fnName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error finding function referenced by the alias: %w", err)
	}

	spec := fv1.FunctionAliasSpec{
		FunctionName:  fnName,
		Version:       input.String(flagkey.AliasVersion),
		PackageDigest: input.String(flagkey.AliasPackageDigest),
	}
	if input.IsSet(flagkey.AliasWeight) {
		w := input.Int(flagkey.AliasWeight)
		spec.Weight = &w
	}
	if input.IsSet(flagkey.AliasSecondaryVersion) {
		spec.SecondaryVersion = input.String(flagkey.AliasSecondaryVersion)
	}

	opts.alias = &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				fv1.VersionFunctionNameLabel: fn.Name,
			},
			OwnerReferences: []metav1.OwnerReference{fv1.FunctionOwnerRef(fn)},
		},
		Spec: spec,
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(opts.alias.Namespace).Create(input.Context(), opts.alias, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating function alias: %w", err)
	}

	fmt.Printf("function alias '%v' created\n", opts.alias.Name)
	return nil
}
