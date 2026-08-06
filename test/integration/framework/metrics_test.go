// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricHasLabels(t *testing.T) {
	t.Parallel()
	raw := []byte(`# HELP fission_router_endpointcache_mode Always 1.
# TYPE fission_router_endpointcache_mode gauge
fission_router_endpointcache_mode{effective="off",requested="on"} 1
fission_router_endpointcache_mode{effective="on",requested="on",replica="second"} 1
`)
	tests := []struct {
		name string
		want map[string]string
		has  bool
	}{
		{name: "finds exact sample", want: map[string]string{"effective": "on", "requested": "on"}, has: true},
		{name: "allows extra labels", want: map[string]string{"replica": "second"}, has: true},
		{name: "rejects different value", want: map[string]string{"effective": "missing"}, has: false},
		{name: "rejects missing label", want: map[string]string{"namespace": "default"}, has: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MetricHasLabels(raw, "fission_router_endpointcache_mode", tt.want)
			require.NoError(t, err)
			assert.Equal(t, tt.has, got)
		})
	}
}

func TestMetricHasLabelsRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	_, err := MetricHasLabels([]byte("not valid prometheus text\n"), "metric", nil)
	require.Error(t, err)
}
