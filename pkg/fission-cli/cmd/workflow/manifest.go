// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// parseManifest reads the --file manifest: either a full Workflow (kind:
// Workflow) or a bare WorkflowSpec. --name, when set, overrides the
// manifest's metadata.name. The namespace is left as the manifest gave it;
// callers resolve an empty one against the global --namespace flag.
func parseManifest(input cli.Input) (*fv1.Workflow, error) {
	path := input.String(flagkey.WfFile)
	if path == "" {
		return nil, errors.New("need a manifest file, use --file/-f")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Sniff the kind to decide full-manifest vs bare-spec.
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	var wf fv1.Workflow
	switch probe.Kind {
	case "Workflow":
		if err := yaml.UnmarshalStrict(data, &wf); err != nil {
			return nil, fmt.Errorf("parsing %s as a Workflow manifest: %w", path, err)
		}
	case "":
		var spec fv1.WorkflowSpec
		if err := yaml.UnmarshalStrict(data, &spec); err != nil {
			return nil, fmt.Errorf("parsing %s as a WorkflowSpec: %w", path, err)
		}
		wf.Spec = spec
	default:
		return nil, fmt.Errorf("%s: unexpected kind %q (want Workflow or a bare WorkflowSpec)", path, probe.Kind)
	}

	if name := input.String(flagkey.WfName); name != "" {
		wf.Name = name
	}
	// Same defaulting the mutating webhook applies (function type -> "name"),
	// so offline validation matches admission.
	wf.Spec.ApplyDefaults()
	return &wf, nil
}

// loadWorkflow is parseManifest plus the name requirement — for commands
// that write to the cluster (create/update).
func loadWorkflow(input cli.Input) (*fv1.Workflow, error) {
	wf, err := parseManifest(input)
	if err != nil {
		return nil, err
	}
	if wf.Name == "" {
		return nil, errors.New("the manifest has no metadata.name; provide one or use --name")
	}
	return wf, nil
}
