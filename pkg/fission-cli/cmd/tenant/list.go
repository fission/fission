// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

type ListSubCommand struct {
	cmd.CommandActioner
}

func (opts *ListSubCommand) do(input cli.Input) error {
	tenants, err := opts.Client().FissionClientSet.CoreV1().FissionTenants().List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing tenants: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "NAMESPACE", "FUNCTION-NS", "READY", "SOURCE"}
	row := func(ft v1.FissionTenant) []string {
		fnNS := ft.Spec.FunctionNamespace
		if fnNS == "" {
			fnNS = "-"
		}
		return []string{
			ft.Name,
			ft.Spec.Namespace,
			fnNS,
			util.ConditionStatus(ft.Status.Conditions, v1.FissionTenantConditionReady),
			managedBySource(ft.Annotations),
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(ft v1.FissionTenant) []string { return []string{util.AgeOf(ft.CreationTimestamp)} }

	return util.PrintObjects(format, tenants.Items, headers, row, wideExtra, wideRow)
}
