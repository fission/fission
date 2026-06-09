// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ToolsSubCommand struct {
	cmd.CommandActioner
}

// Tools lists the functions advertised as MCP tools. It reads the Function CRDs
// directly (like the rest of the CLI); no MCP server round-trip is needed since
// the tool contract is declarative on the CRD.
func Tools(input cli.Input) error {
	return (&ToolsSubCommand{}).do(input)
}

func (opts *ToolsSubCommand) do(input cli.Input) error {
	namespace, err := opts.ResolveNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return fmt.Errorf("error listing MCP tools: %w", err)
	}

	fns, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing functions: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	exposed := make([]fv1.Function, 0, len(fns.Items))
	for _, f := range fns.Items {
		if f.Spec.Tool != nil && f.Spec.Tool.ExposeAsMCP {
			exposed = append(exposed, f)
		}
	}

	headers := []string{"TOOL", "FUNCTION", "NAMESPACE", "DESCRIPTION", "EXPOSED"}
	row := func(f fv1.Function) []string {
		toolName := f.Spec.Tool.ToolName
		if toolName == "" {
			toolName = f.Namespace + "-" + f.Name
		}
		return []string{
			toolName,
			f.Name,
			f.Namespace,
			f.Spec.Tool.Description,
			util.ConditionStatus(f.Status.Conditions, fv1.FunctionConditionToolExposed),
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(f fv1.Function) []string { return []string{util.AgeOf(f.CreationTimestamp)} }

	return util.PrintObjects(format, exposed, headers, row, wideExtra, wideRow)
}
