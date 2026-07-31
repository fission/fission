// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package crds embeds the generated CustomResourceDefinition manifests so a
// binary can apply the exact schemas it was built against (RFC-0029 §1).
//
// The embed lives here rather than in the consuming command because go:embed
// cannot reach outside its own package directory, and it guarantees the
// binary and the schemas ship as one versioned artifact: the pre-upgrade hook
// image is a single-binary chainguard/static image with no filesystem to
// carry YAML alongside it.
package crds

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// manifests holds the generated CRD YAMLs. kustomization.yaml is deliberately
// not embedded — it is build tooling, not a manifest to apply.
//
//go:embed v1/fission.io_*.yaml
var manifests embed.FS

// Manifest is one embedded CRD document.
type Manifest struct {
	// Name is the file name, for error messages and logs.
	Name string
	// YAML is the document bytes exactly as generated.
	YAML []byte
}

// All returns every embedded CRD manifest, sorted by file name so apply order
// is deterministic across runs.
func All() ([]Manifest, error) {
	entries, err := fs.ReadDir(manifests, "v1")
	if err != nil {
		return nil, fmt.Errorf("read embedded CRD directory: %w", err)
	}
	out := make([]Manifest, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := fs.ReadFile(manifests, "v1/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded CRD %s: %w", e.Name(), err)
		}
		out = append(out, Manifest{Name: e.Name(), YAML: data})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no CRD manifests embedded: the build did not include crds/v1")
	}
	return out, nil
}
