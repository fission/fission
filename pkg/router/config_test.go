// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// setRequiredRouterEnv sets the env vars loadRouterConfig hard-fails without.
// Tests using it must not call t.Parallel (t.Setenv restriction).
func setRequiredRouterEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ROUTER_ROUND_TRIP_TIMEOUT", "50ms")
	t.Setenv("ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT", "2")
	t.Setenv("ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME", "30s")
	t.Setenv("ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE", "true")
	t.Setenv("ROUTER_ROUND_TRIP_MAX_RETRIES", "10")
	t.Setenv("DEBUG_ENV", "false")
	t.Setenv("USE_ENCODED_PATH", "false")
}

func TestLoadRouterConfig(t *testing.T) {
	setRequiredRouterEnv(t)
	t.Setenv("ROUTER_SVC_ADDRESS_MAX_RETRIES", "7")
	t.Setenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT", "45s")
	t.Setenv("ROUTER_UNTAP_SERVICE_TIMEOUT", "120s")
	t.Setenv("ROUTER_STREAM_IDLE_TIMEOUT", "90s")

	cfg, err := loadRouterConfig(logr.Discard())
	require.NoError(t, err)

	assert.Equal(t, 50*time.Millisecond, cfg.roundTripTimeout)
	assert.Equal(t, 2, cfg.timeoutExponent)
	assert.Equal(t, 30*time.Second, cfg.keepAliveTime)
	assert.True(t, cfg.disableKeepAlive)
	assert.Equal(t, 10, cfg.maxRetries)
	assert.False(t, cfg.isDebugEnv)
	assert.False(t, cfg.useEncodedPath)
	assert.Equal(t, 7, cfg.svcAddrRetryCount)
	assert.Equal(t, 45*time.Second, cfg.svcAddrUpdateTimeout)
	assert.Equal(t, 120*time.Second, cfg.unTapServiceTimeout)
	assert.Equal(t, 90*time.Second, cfg.streamIdleDefault)
}

// TestLoadRouterConfigDefaults locks the soft-fail fields: unset (or
// unparsable) values fall back to defaults instead of erroring.
func TestLoadRouterConfigDefaults(t *testing.T) {
	setRequiredRouterEnv(t)

	cfg, err := loadRouterConfig(logr.Discard())
	require.NoError(t, err)

	assert.Equal(t, 5, cfg.svcAddrRetryCount)
	assert.Equal(t, 30*time.Second, cfg.svcAddrUpdateTimeout)
	assert.Equal(t, 3600*time.Second, cfg.unTapServiceTimeout)
	assert.Equal(t, time.Duration(fv1.DefaultStreamIdleSeconds)*time.Second, cfg.streamIdleDefault)
}

// TestLoadRouterConfigHardFailures locks the hard-fail fields: a missing or
// invalid value aborts startup.
func TestLoadRouterConfigHardFailures(t *testing.T) {
	t.Run("missing round trip timeout", func(t *testing.T) {
		setRequiredRouterEnv(t)
		t.Setenv("ROUTER_ROUND_TRIP_TIMEOUT", "")
		_, err := loadRouterConfig(logr.Discard())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ROUTER_ROUND_TRIP_TIMEOUT")
	})

	t.Run("invalid max retries", func(t *testing.T) {
		setRequiredRouterEnv(t)
		t.Setenv("ROUTER_ROUND_TRIP_MAX_RETRIES", "not-a-number")
		_, err := loadRouterConfig(logr.Discard())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ROUTER_ROUND_TRIP_MAX_RETRIES")
	})

	t.Run("non-positive stream idle timeout", func(t *testing.T) {
		setRequiredRouterEnv(t)
		t.Setenv("ROUTER_STREAM_IDLE_TIMEOUT", "-5s")
		_, err := loadRouterConfig(logr.Discard())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ROUTER_STREAM_IDLE_TIMEOUT")
	})
}

// TestLoadRouterConfigEndpointSliceMode locks the tri-state gate. The
// load-bearing case is unset → off: that is the no-behavior-change guarantee
// for every existing install (CI pins the gate explicitly, so a default
// flipping would not be caught there).
func TestLoadRouterConfigEndpointSliceMode(t *testing.T) {
	cases := []struct {
		value   string
		want    endpointSliceCacheMode
		wantErr bool
	}{
		{value: "", want: endpointSliceCacheOff},
		{value: "off", want: endpointSliceCacheOff},
		{value: "shadow", wantErr: true}, // removed with the phase-4 defaults flip
		{value: "on", want: endpointSliceCacheOn},
		{value: "On", wantErr: true}, // no case folding: fail loud, not silently legacy
		{value: "bogus", wantErr: true},
	}
	for _, tc := range cases {
		t.Run("mode="+tc.value, func(t *testing.T) {
			setRequiredRouterEnv(t)
			t.Setenv("ROUTER_ENDPOINTSLICE_CACHE_MODE", tc.value)
			cfg, err := loadRouterConfig(logr.Discard())
			if tc.wantErr {
				require.Error(t, err, "an unrecognized mode must abort startup")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.endpointSliceCacheMode)
		})
	}
}
