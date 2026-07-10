// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/fission/fission/test/benchmark/pkg/scenario"
)

// loadParams reads scenario params from a YAML file. An empty path returns a
// zero Params (BuildAll fills in defaults). Durations are decoded from
// human-friendly strings ("60s") by scenario.Duration.
func loadParams(path string) (scenario.Params, error) {
	var p scenario.Params
	if path == "" {
		return p, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := yaml.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("parse %s: %w", path, err)
	}
	return p, nil
}
