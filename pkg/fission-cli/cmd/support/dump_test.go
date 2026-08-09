// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/labels"
)

// svcLabel matches the literal `svc: <name>` the chart stamps on each
// control-plane Deployment. The templates are Helm, not plain YAML, so this
// reads the labels textually rather than unmarshalling.
var svcLabel = regexp.MustCompile(`(?m)^\s*svc:\s*([a-z0-9-]+)\s*$`)

// A component missing from componentSelector has no spec, no pods and no logs
// in any support bundle, silently — which is how eight of them went missing.
// Pin the selector to what the chart actually ships.
func TestComponentSelectorCoversEveryChartComponent(t *testing.T) {
	t.Parallel()

	sel, err := labels.Parse(componentSelector)
	require.NoError(t, err)

	root := filepath.Join("..", "..", "..", "..", "charts", "fission-all", "templates")
	files, err := filepath.Glob(filepath.Join(root, "*", "deployment.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "chart templates not found under %v", root)

	seen := 0
	for _, f := range files {
		content, err := os.ReadFile(f) //nolint:gosec // fixed path inside the repo
		require.NoError(t, err)

		for _, m := range svcLabel.FindAllStringSubmatch(string(content), -1) {
			seen++
			assert.True(t, sel.Matches(labels.Set{"svc": m[1]}),
				"%v ships svc=%q, which componentSelector does not match — that component's "+
					"spec and logs would be missing from every support bundle", f, m[1])
		}
	}
	require.NotZero(t, seen, "found no svc: labels; the regex or the chart layout changed")
}

// The three execution backends must all be collectable. container was missing
// from the function selectors, so container-executor functions had no spec,
// pods or logs in a bundle.
func TestFunctionSelectorsCoverEveryExecutorType(t *testing.T) {
	t.Parallel()

	all, err := labels.Parse(functionSelector)
	require.NoError(t, err)
	for _, et := range []string{"poolmgr", "newdeploy", "container"} {
		assert.True(t, all.Matches(labels.Set{"executorType": et}), "functionSelector misses %v", et)
	}

	// Narrower on purpose: poolmgr serves from the shared pool and creates no
	// per-function Service, so it must not be here.
	svc, err := labels.Parse(functionSvcSelector)
	require.NoError(t, err)
	assert.False(t, svc.Matches(labels.Set{"executorType": "poolmgr"}),
		"poolmgr creates no per-function Service")
	for _, et := range []string{"newdeploy", "container"} {
		assert.True(t, svc.Matches(labels.Set{"executorType": et}), "functionSvcSelector misses %v", et)
	}
}
