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
}
