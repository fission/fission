// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type RunSubCommand struct {
	cmd.CommandActioner
}

// Run starts one execution of a workflow: it reads the Workflow (recording
// its generation for observability — the engine snapshots the authoritative
// spec into the run's stream) and creates a WorkflowRun.
func Run(input cli.Input) error {
	return (&RunSubCommand{}).do(input)
}

func (opts *RunSubCommand) do(input cli.Input) error {
	wfName := input.String(flagkey.WfName)
	if wfName == "" {
		return errors.New("need the workflow to run, use --name")
	}

	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in running workflow: %w", err)
	}

	wf, err := opts.Client().FissionClientSet.CoreV1().Workflows(namespace).Get(input.Context(), wfName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting workflow %q: %w", wfName, err)
	}

	runInput, err := readRunInput(input)
	if err != nil {
		return err
	}

	run := &fv1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", wfName, rand.String(5)),
			Namespace: namespace,
		},
		Spec: fv1.WorkflowRunSpec{
			WorkflowRef:        wfName,
			WorkflowGeneration: wf.Generation,
			Input:              runInput,
		},
	}

	created, err := opts.Client().FissionClientSet.CoreV1().WorkflowRuns(namespace).Create(input.Context(), run, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating workflow run: %w", err)
	}

	// A run with no controller sits Pending forever with no signal — the
	// known admission-accepts-what-runtime-can't-service failure mode. Check
	// the head is deployed and say so now, while the user is looking.
	warnIfNoController(input, opts)

	fmt.Printf("workflow run '%v' started\n", created.Name)
	return nil
}

// readRunInput parses --input: inline JSON, or @path to a file.
func readRunInput(input cli.Input) (*apiextensionsv1.JSON, error) {
	raw := input.String(flagkey.WfInput)
	if raw == "" {
		return nil, nil
	}
	data := []byte(raw)
	if strings.HasPrefix(raw, "@") {
		var err error
		data, err = os.ReadFile(strings.TrimPrefix(raw, "@"))
		if err != nil {
			return nil, fmt.Errorf("reading input file: %w", err)
		}
	}
	if !json.Valid(data) {
		return nil, errors.New("--input must be valid JSON (or @file containing JSON)")
	}
	return &apiextensionsv1.JSON{Raw: data}, nil
}

// warnIfNoController checks the workflow Deployment has ready replicas.
// Best-effort: RBAC or install-layout differences degrade to silence, never
// to a failed run creation.
func warnIfNoController(input cli.Input, opts *RunSubCommand) {
	deploy, err := opts.Client().KubernetesClient.AppsV1().Deployments(fissionNamespace()).
		Get(input.Context(), "workflow", metav1.GetOptions{})
	if err != nil || deploy.Status.ReadyReplicas == 0 {
		console.Warn("no workflow controller appears to be running (is workflows.enabled set?); the run will sit Pending until one accepts it")
	}
}
