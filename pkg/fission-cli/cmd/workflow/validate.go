// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"
	"maps"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type ValidateSubCommand struct {
	cmd.CommandActioner
}

// Validate lints a workflow manifest: the full offline rule set (graph,
// expressions — the same checks admission enforces), plus referenced-function
// existence against the cluster unless --offline. Function existence is a
// warning, never an error: GitOps applies resources in arbitrary order.
func Validate(input cli.Input) error {
	return (&ValidateSubCommand{}).do(input)
}

func (opts *ValidateSubCommand) do(input cli.Input) error {
	wf, err := parseManifest(input)
	if err != nil {
		return err
	}

	// Run the same rule set admission enforces — a linter that passes what
	// the real gate rejects is a broken contract. A name is optional here
	// (bare-spec files are legal input), but when present it must be valid.
	if err := wf.Spec.Validate(); err != nil {
		return fv1.AggregateValidationErrors("Workflow", err)
	}
	name := wf.Name
	if name == "" {
		name = input.String(flagkey.WfFile)
	} else if err := fv1.ValidateKubeName("Workflow.Name", wf.Name); err != nil {
		return fv1.AggregateValidationErrors("Workflow", err)
	}

	switch {
	case input.Bool(flagkey.WfOffline):
		// Offline: skip cluster checks by request.
	case !opts.ClusterAvailable():
		// The command is cluster-optional (see command.go): without a usable
		// kubeconfig the client is nil, so degrade to offline with a note
		// instead of dereferencing it.
		console.Warn("no Kubernetes cluster configured; skipping referenced-function existence checks (as if --offline)")
	default:
		// Check functions where the workflow will actually live: the
		// manifest's namespace wins (mirroring create), the global flag is
		// the fallback.
		namespace := wf.Namespace
		if namespace == "" {
			_, ns, err := opts.GetResourceNamespace(input)
			if err != nil {
				return fmt.Errorf("error resolving namespace: %w", err)
			}
			namespace = ns
		}
		// One List instead of a Get per state: a workflow may carry up to
		// MaxWorkflowStates task states, several typically sharing functions.
		fns, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).List(input.Context(), metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing functions in namespace %q: %w", namespace, err)
		}
		exists := make(map[string]bool, len(fns.Items))
		for _, fn := range fns.Items {
			exists[fn.Name] = true
		}
		for _, state := range slices.Sorted(maps.Keys(wf.Spec.States)) {
			st := wf.Spec.States[state]
			if st.Function == nil || st.Function.Name == "" || exists[st.Function.Name] {
				continue
			}
			console.Warn(fmt.Sprintf("state %q references function %q which does not exist in namespace %q (create it before running the workflow)",
				state, st.Function.Name, namespace))
		}
	}

	fmt.Printf("workflow %s: valid\n", name)
	return nil
}
