// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/crds"
)

// TestEmbeddedCRDsAreApplyable is the packaging guard: the hook image is a
// single static binary with no filesystem, so if the embed ever stops picking
// up crds/v1 the failure must surface here rather than as an install that
// silently ships no schemas.
func TestEmbeddedCRDsAreApplyable(t *testing.T) {
	t.Parallel()

	manifests, err := crds.All()
	require.NoError(t, err)
	require.NotEmpty(t, manifests, "no CRDs embedded")

	names := make(map[string]struct{}, len(manifests))
	for _, m := range manifests {
		name, body, err := crdApplyBody(m)
		require.NoError(t, err, m.Name)
		assert.NotEmpty(t, name)
		names[name] = struct{}{}

		// The apply body must be valid JSON carrying the same identity, or
		// the apiserver rejects the patch.
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded), m.Name)
		assert.Equal(t, "apiextensions.k8s.io/v1", decoded["apiVersion"], m.Name)
		assert.Equal(t, "CustomResourceDefinition", decoded["kind"], m.Name)
	}

	// Spot-check the CRDs the control plane cannot start without, so a
	// partial embed (e.g. a narrowed glob) fails loudly.
	for _, want := range []string{
		"functions.fission.io", "packages.fission.io", "environments.fission.io",
		"httptriggers.fission.io", "messagequeuetriggers.fission.io",
	} {
		assert.Contains(t, names, want)
	}
}

func TestCRDApplyBodyRejectsNonFissionAndConversion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "non-fission group",
			yaml: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names: {plural: widgets, kind: Widget}
`,
		},
		{
			name: "conversion webhook",
			yaml: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: functions.fission.io
spec:
  group: fission.io
  scope: Namespaced
  names: {plural: functions, kind: Function}
  conversion:
    strategy: Webhook
`,
		},
		{
			name: "not a CRD",
			yaml: `apiVersion: v1
kind: ConfigMap
metadata:
  name: nope
`,
		},
		{
			name: "no name",
			yaml: `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  group: fission.io
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := crdApplyBody(crds.Manifest{Name: tc.name, YAML: []byte(tc.yaml)})
			assert.Error(t, err)
		})
	}
}

// TestChartCRDNamesMatchEmbeddedBundle guards the one piece of this design
// that is hand-maintained: the chart's RBAC fences the hook's mutating verbs
// to an explicit list of CRD names, while the binary discovers its bundle by
// glob. Add a CRD and forget the helper and nothing fails until a user's
// upgrade dies with a 403 on the release that introduces the new schema.
func TestChartCRDNamesMatchEmbeddedBundle(t *testing.T) {
	t.Parallel()

	helpers, err := os.ReadFile(filepath.Join("..", "..", "charts", "fission-all", "templates", "_helpers.tpl"))
	require.NoError(t, err)

	const marker = `{{- define "fission.crdNames" -}}`
	idx := strings.Index(string(helpers), marker)
	require.GreaterOrEqual(t, idx, 0, "fission.crdNames helper not found in _helpers.tpl")
	rest := string(helpers)[idx+len(marker):]
	body, _, found := strings.Cut(rest, "{{- end -}}")
	require.True(t, found, "fission.crdNames helper is not terminated")

	chartNames := strings.Fields(body)

	manifests, err := crds.All()
	require.NoError(t, err)
	embedded := make([]string, 0, len(manifests))
	for _, m := range manifests {
		name, _, err := crdApplyBody(m)
		require.NoError(t, err)
		embedded = append(embedded, name)
	}

	sort.Strings(chartNames)
	sort.Strings(embedded)
	assert.Equal(t, embedded, chartNames,
		"fission.crdNames in charts/fission-all/templates/_helpers.tpl must list exactly the CRDs embedded from crds/v1")
}
