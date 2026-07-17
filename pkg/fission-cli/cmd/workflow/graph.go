// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"fmt"

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

	diagram, classes := renderMermaid(spec, nil)
	if input.Bool(flagkey.WfOpen) {
		title := input.String(flagkey.WfName)
		if title == "" {
			title = "workflow"
		}
		return serveDiagram(input.Context(), pageData{
			Title:   title,
			Diagram: diagram,
			Legend:  legendFor(classes, false),
		})
	}
	fmt.Println(diagram)
	return nil
}
