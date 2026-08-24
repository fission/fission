// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

// TestDashboardsRenderAndParse guards a footgun that neither `helm lint` nor the
// lint-dashboards CI job catches.
//
// templates/dashboards/dashboards.yaml embeds each dashboard as a SINGLE-QUOTED
// YAML scalar:
//
//	{{ base $path }}: '{{ $files.Get $path }}'
//
// so a single apostrophe anywhere in a dashboard — in a panel description, a
// legend, a title — closes the scalar early and the whole chart stops rendering.
// The failure is a YAML parse error pointing at a line number inside the
// embedded JSON, which says nothing about the apostrophe that caused it.
//
// lint-dashboards.sh does not catch it because it validates the JSON files
// directly, never through the chart. `helm lint` does not either, because the
// dashboards are off by default. This test renders them on and parses the JSON
// back out of the ConfigMap, which fails on both.
func TestDashboardsRenderAndParse(t *testing.T) {
	ms := render(t, "--set", "grafana.dashboards.enable=true")

	configMaps := ms.ofKind("ConfigMap")
	var dashboards int
	for i := range configMaps {
		m := &configMaps[i]
		if !strings.Contains(m.Name, "dashboard") {
			continue
		}
		cm := decodeAs[corev1.ConfigMap](t, m)
		for file, body := range cm.Data {
			dashboards++
			// A struct naming the one field this test reads, rather than
			// map[string]any: the assertion is "it parses and has panels", and
			// decoding the rest untyped would claim knowledge of a Grafana
			// schema this test neither has nor checks.
			var parsed struct {
				Panels []json.RawMessage `json:"panels"`
			}
			require.NoErrorf(t, json.Unmarshal([]byte(body), &parsed),
				"%s must survive the ConfigMap round-trip as valid JSON", file)
			assert.NotEmptyf(t, parsed.Panels, "%s must carry panels", file)
		}
	}
	require.NotZero(t, dashboards, "the chart must render its dashboards when grafana.dashboards.enable=true")
}
