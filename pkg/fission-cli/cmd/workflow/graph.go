// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type GraphSubCommand struct {
	cmd.CommandActioner
}

// Graph renders the state machine as mermaid, from a manifest file (--file)
// or a workflow on the cluster (--name).
func Graph(input cli.Input) error {
	return (&GraphSubCommand{}).do(input)
}

func (opts *GraphSubCommand) do(input cli.Input) error {
	var spec fv1.WorkflowSpec

	if input.String(flagkey.WfFile) != "" && input.String(flagkey.WfName) != "" {
		return errors.New("--file and --name are mutually exclusive; render from the manifest or from the cluster, not both")
	}

	switch {
	case input.String(flagkey.WfFile) != "":
		wf, err := parseManifest(input)
		if err != nil {
			return err
		}
		spec = wf.Spec
	case input.String(flagkey.WfName) != "":
		// The command is cluster-optional (see command.go): --name needs the
		// cluster, so fail clearly instead of dereferencing a nil client.
		if !opts.ClusterAvailable() {
			return errors.New("no Kubernetes cluster configured; use --file to render from a manifest, or set up a kubeconfig")
		}
		_, namespace, err := opts.GetResourceNamespace(input)
		if err != nil {
			return fmt.Errorf("error in rendering workflow graph: %w", err)
		}
		wf, err := opts.Client().FissionClientSet.CoreV1().Workflows(namespace).Get(input.Context(), input.String(flagkey.WfName), metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error getting workflow: %w", err)
		}
		spec = wf.Spec
	default:
		return errors.New("need a workflow, use --name or --file")
	}

	// An empty spec renders a broken diagram with exit 0 — refuse it.
	if spec.StartAt == "" || len(spec.States) == 0 {
		return errors.New("the manifest has no states; nothing to render")
	}

	fmt.Println(mermaidFromSpec(spec))
	return nil
}

// mermaidFromSpec renders a stateDiagram-v2. Output is deterministic: states
// are emitted in sorted order.
func mermaidFromSpec(spec fv1.WorkflowSpec) string {
	var b strings.Builder
	b.WriteString("stateDiagram-v2\n")
	fmt.Fprintf(&b, "    [*] --> %s\n", spec.StartAt)

	names := make([]string, 0, len(spec.States))
	for name := range spec.States {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		st := spec.States[name]
		if st.Next != "" {
			fmt.Fprintf(&b, "    %s --> %s\n", name, st.Next)
		}
		for i, c := range st.Choices {
			fmt.Fprintf(&b, "    %s --> %s : rule %d\n", name, c.Next, i+1)
		}
		if st.Default != "" {
			fmt.Fprintf(&b, "    %s --> %s : default\n", name, st.Default)
		}
		for _, c := range st.Catch {
			fmt.Fprintf(&b, "    %s --> %s : %s\n", name, c.Next, c.ErrorType)
		}
		if st.End || st.Type == fv1.WorkflowStateSucceed || st.Type == fv1.WorkflowStateFail {
			fmt.Fprintf(&b, "    %s --> [*]\n", name)
		}
	}
	return b.String()
}
