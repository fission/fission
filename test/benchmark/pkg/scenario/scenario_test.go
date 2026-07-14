// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestPercentile(t *testing.T) {
	t.Parallel()
	samples := []time.Duration{
		50 * time.Millisecond, 10 * time.Millisecond, 40 * time.Millisecond,
		20 * time.Millisecond, 30 * time.Millisecond,
	}
	assert.Equal(t, 50*time.Millisecond, percentile(samples, 100))
	assert.Equal(t, 10*time.Millisecond, percentile(samples, 0))
	assert.Zero(t, percentile(nil, 95))
}

func TestSizeLabel(t *testing.T) {
	t.Parallel()
	cases := map[int]string{
		512:       "512B",
		1 << 10:   "1KiB",
		1536:      "1536B", // not an exact KiB multiple -> exact bytes, no collision
		100 << 10: "100KiB",
		1 << 20:   "1MiB",
	}
	for size, want := range cases {
		assert.Equalf(t, want, sizeLabel(size), "sizeLabel(%d)", size)
	}
}

func TestSelect(t *testing.T) {
	t.Parallel()
	all := BuildAll(DefaultParams())

	byName := Select(all, []string{"warm-path"}, nil)
	require.Len(t, byName, 1)
	assert.Equal(t, "warm-path", byName[0].Name())

	smoke := Names(Select(all, nil, []string{"smoke"}))
	assert.Contains(t, smoke, "warm-path")
	assert.Contains(t, smoke, "cold-start-poolmgr")

	assert.Len(t, Select(all, nil, nil), len(all), "empty filter returns all")
}

func TestBuildAllRespectsExecutors(t *testing.T) {
	t.Parallel()
	names := Names(BuildAll(Params{Executors: []fv1.ExecutorType{fv1.ExecutorTypePoolmgr}}))
	assert.Contains(t, names, "cold-start-poolmgr")
	assert.NotContains(t, names, "cold-start-newdeploy")
}

func TestParamsNormalizeFillsDefaults(t *testing.T) {
	t.Parallel()
	p := Params{ColdIterations: 5}.normalize()
	assert.Equal(t, 5, p.ColdIterations, "explicit value preserved")
	assert.Equal(t, DefaultParams().Poolsize, p.Poolsize, "zero value defaulted")
	assert.Equal(t, DefaultParams().WarmDuration, p.WarmDuration, "zero duration defaulted")
	assert.Equal(t, DefaultParams().ConfigDepsSecrets, p.ConfigDepsSecrets, "config-deps secret count defaulted")
	assert.Equal(t, DefaultParams().ConfigDepsConfigMaps, p.ConfigDepsConfigMaps, "config-deps configmap count defaulted")
}

// TestConfigDepsScenarioRegistered pins that the secret/configmap cold-start
// scenario is built and is deliberately excluded from the smoke subset (its
// few-ms per-reference delta needs the repeated full run to clear the noise
// floor), and that it carries its configured reference counts.
func TestConfigDepsScenarioRegistered(t *testing.T) {
	t.Parallel()
	all := BuildAll(DefaultParams())
	assert.Contains(t, Names(all), "cold-start-poolmgr-configdeps")
	assert.NotContains(t, Names(Select(all, nil, []string{"smoke"})), "cold-start-poolmgr-configdeps",
		"config-deps scenario must stay out of the noise-sensitive smoke subset")

	built := Select(BuildAll(Params{ConfigDepsSecrets: 2, ConfigDepsConfigMaps: 3}),
		[]string{"cold-start-poolmgr-configdeps"}, nil)
	require.Len(t, built, 1)
	cd, ok := built[0].(*coldStartConfigDeps)
	require.True(t, ok)
	assert.Equal(t, 2, cd.secrets)
	assert.Equal(t, 3, cd.configMaps)
}

// TestAsyncInvokeScenarioRegistered pins that the RFC-0024 async scenario is built
// and is deliberately OFF the smoke subset (it needs a warm drain window, so it
// runs in the weekly/dispatch suite, not per-PR).
func TestAsyncInvokeScenarioRegistered(t *testing.T) {
	t.Parallel()
	all := BuildAll(DefaultParams())
	assert.Contains(t, Names(all), "async-invoke")
	assert.NotContains(t, Names(Select(all, nil, []string{"smoke"})), "async-invoke",
		"async-invoke must stay out of the fast per-PR smoke subset")
}

func TestColdBurstScenariosRegistered(t *testing.T) {
	t.Parallel()
	names := Names(BuildAll(DefaultParams()))
	assert.Contains(t, names, "cold-burst-same-fn")
	assert.Contains(t, names, "cold-burst-distinct-fn")
	// The default burst must exceed the default pool, or the scenario silently
	// stops exercising exhaustion/refill.
	p := DefaultParams()
	assert.Greater(t, p.BurstSize, p.Poolsize)
}

func TestWarmPathPerExecutor(t *testing.T) {
	t.Parallel()
	names := Names(BuildAll(DefaultParams()))
	assert.Contains(t, names, "warm-path")
	assert.Contains(t, names, "warm-path-newdeploy")
	// Only the poolmgr variant runs in the per-PR smoke.
	for _, s := range BuildAll(DefaultParams()) {
		if s.Name() == "warm-path-newdeploy" {
			assert.NotContains(t, s.Tags(), "smoke")
		}
	}
}
