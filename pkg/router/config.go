// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// endpointSliceCacheMode selects how the router uses its EndpointSlice-fed
// endpoint index (RFC-0002): off (never watch slices — the legacy
// executor-RPC data plane) or on (the index is the warm-path address source;
// the chart default since the phase-4 flip). The env-level default stays off:
// unset means off, so raw-env deployments keep legacy behavior.
type endpointSliceCacheMode string

const (
	endpointSliceCacheOff endpointSliceCacheMode = "off"
	endpointSliceCacheOn  endpointSliceCacheMode = "on"
)

// routerConfig collects every ROUTER_* (plus DEBUG_ENV / USE_ENCODED_PATH)
// environment setting parsed at startup. Hard-fail fields abort startup when
// missing or unparsable; soft-fail fields log and fall back to a default.
type routerConfig struct {
	// hard-fail fields
	roundTripTimeout time.Duration
	timeoutExponent  int
	keepAliveTime    time.Duration
	disableKeepAlive bool
	maxRetries       int
	isDebugEnv       bool
	useEncodedPath   bool
	// streamIdleDefault is the idle timeout applied to streaming functions when
	// StreamingConfig.IdleTimeoutSeconds is unset. Optional env; invalid or
	// non-positive values abort startup.
	streamIdleDefault time.Duration

	// endpointSliceCacheMode gates the EndpointSlice-fed endpoint index
	// (ROUTER_ENDPOINTSLICE_CACHE_MODE: off|on; unset means off; an
	// unrecognized value aborts startup).
	endpointSliceCacheMode endpointSliceCacheMode
	// endpointSliceEndpointLB dials newdeploy/container pod IPs directly
	// (ROUTER_ENDPOINTSLICE_ENDPOINT_LB; optional, default false, soft-fail).
	endpointSliceEndpointLB bool

	// soft-fail fields (default on parse error)
	svcAddrRetryCount    int
	svcAddrUpdateTimeout time.Duration
	unTapServiceTimeout  time.Duration
	// maxIdleConnsPerHost bounds the shared proxy transport's idle-connection
	// pool per function address (ROUTER_ROUND_TRIP_MAX_IDLE_CONNS_PER_HOST;
	// optional, 0 = the built-in default, soft-fail).
	maxIdleConnsPerHost int
}

// loadRouterConfig parses the router's environment configuration. Behavior is
// identical to the historical inline parsing in Start: required values
// hard-fail; svcAddrRetryCount, svcAddrUpdateTimeout and unTapServiceTimeout
// log and default on error.
func loadRouterConfig(logger logr.Logger) (routerConfig, error) {
	var cfg routerConfig

	timeoutStr := os.Getenv("ROUTER_ROUND_TRIP_TIMEOUT")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse timeout duration value('%s') from 'ROUTER_ROUND_TRIP_TIMEOUT': %w", timeoutStr, err)
	}
	cfg.roundTripTimeout = timeout

	timeoutExponentStr := os.Getenv("ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT")
	timeoutExponent, err := strconv.Atoi(timeoutExponentStr)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse timeout exponent value('%s') from 'ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT': %w", timeoutExponentStr, err)
	}
	cfg.timeoutExponent = timeoutExponent

	keepAliveTimeStr := os.Getenv("ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME")
	keepAliveTime, err := time.ParseDuration(keepAliveTimeStr)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse keep alive duration value('%s') from 'ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME': %w", keepAliveTimeStr, err)
	}
	cfg.keepAliveTime = keepAliveTime

	disableKeepAliveStr := os.Getenv("ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE")
	disableKeepAlive, err := strconv.ParseBool(disableKeepAliveStr)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse enable keep alive value('%s') from 'ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE': %w", disableKeepAliveStr, err)
	}
	cfg.disableKeepAlive = disableKeepAlive

	maxRetriesStr := os.Getenv("ROUTER_ROUND_TRIP_MAX_RETRIES")
	maxRetries, err := strconv.Atoi(maxRetriesStr)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse max retries value('%s') from 'ROUTER_ROUND_TRIP_MAX_RETRIES': %w", maxRetriesStr, err)
	}
	cfg.maxRetries = maxRetries

	// streamIdleDefault is the idle timeout applied to streaming functions when
	// StreamingConfig.IdleTimeoutSeconds is unset. Optional; defaults to
	// DefaultStreamIdleSeconds.
	cfg.streamIdleDefault = time.Duration(fv1.DefaultStreamIdleSeconds) * time.Second
	if v := os.Getenv("ROUTER_STREAM_IDLE_TIMEOUT"); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			return cfg, fmt.Errorf("failed to parse stream idle timeout value('%s') from 'ROUTER_STREAM_IDLE_TIMEOUT': %w", v, perr)
		}
		// A non-positive idle window would silently disable the streaming idle
		// watchdog (streams with no max-duration ceiling could then hang forever).
		// Reject it at startup rather than failing open.
		if d <= 0 {
			return cfg, fmt.Errorf("'ROUTER_STREAM_IDLE_TIMEOUT' must be a positive duration, got %q", v)
		}
		cfg.streamIdleDefault = d
	}

	isDebugEnvStr := os.Getenv("DEBUG_ENV")
	isDebugEnv, err := strconv.ParseBool(isDebugEnvStr)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse debug env value('%s') from 'DEBUG_ENV': %w", isDebugEnvStr, err)
	}
	cfg.isDebugEnv = isDebugEnv

	// svcAddrRetryCount is the max times for RetryingRoundTripper to retry with a specific service address
	svcAddrRetryCountStr := os.Getenv("ROUTER_SVC_ADDRESS_MAX_RETRIES")
	svcAddrRetryCount, err := strconv.Atoi(svcAddrRetryCountStr)
	if err != nil {
		svcAddrRetryCount = 5
		logger.Error(err, "failed to parse service address retry count from 'ROUTER_SVC_ADDRESS_MAX_RETRIES' - set to the default value", "value", svcAddrRetryCountStr,
			"default", svcAddrRetryCount)
	}
	cfg.svcAddrRetryCount = svcAddrRetryCount

	// svcAddrUpdateTimeout is the timeout setting for a goroutine to wait for the update of a service entry.
	// If the update process cannot be done within the timeout window, consider it failed.
	svcAddrUpdateTimeoutStr := os.Getenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT")
	svcAddrUpdateTimeout, err := time.ParseDuration(svcAddrUpdateTimeoutStr)
	if err != nil {
		svcAddrUpdateTimeout = 30 * time.Second
		logger.Error(err, "failed to parse service address update timeout duration from 'ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT' - set to the default value", "value", svcAddrUpdateTimeoutStr,
			"default", svcAddrUpdateTimeout)
	}
	cfg.svcAddrUpdateTimeout = svcAddrUpdateTimeout

	// unTapServiceTimeout is the timeout used as timeout in the request context of unTapService
	unTapServiceTimeoutstr := os.Getenv("ROUTER_UNTAP_SERVICE_TIMEOUT")
	unTapServiceTimeout, err := time.ParseDuration(unTapServiceTimeoutstr)
	if err != nil {
		unTapServiceTimeout = 3600 * time.Second
		logger.Error(err, "failed to parse unTap service timeout duration from 'ROUTER_UNTAP_SERVICE_TIMEOUT' - set to the default value", "value", unTapServiceTimeoutstr,
			"default", unTapServiceTimeout)
	}
	cfg.unTapServiceTimeout = unTapServiceTimeout

	// see issue https://github.com/fission/fission/issues/1317
	useEncodedPath, err := strconv.ParseBool(os.Getenv("USE_ENCODED_PATH"))
	if err != nil {
		return cfg, fmt.Errorf("failed to parse USE_ENCODED_PATH: %w", err)
	}
	cfg.useEncodedPath = useEncodedPath

	// Optional pooled-transport tuning (RFC-0014); unset or unparsable keeps
	// the built-in default — a sizing knob, not a correctness gate.
	if raw := os.Getenv("ROUTER_ROUND_TRIP_MAX_IDLE_CONNS_PER_HOST"); raw != "" {
		perHost, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			logger.Error(err, "failed to parse 'ROUTER_ROUND_TRIP_MAX_IDLE_CONNS_PER_HOST' - using the default", "value", raw)
		case perHost < 0:
			logger.Error(nil, "'ROUTER_ROUND_TRIP_MAX_IDLE_CONNS_PER_HOST' must not be negative - using the default", "value", raw)
		default:
			cfg.maxIdleConnsPerHost = perHost
		}
	}

	// Optional; unset or unparsable means off — the flag is an optimization
	// (per-endpoint dialing for newdeploy/container), not a correctness gate
	// like the cache mode, so it soft-fails.
	if raw := os.Getenv("ROUTER_ENDPOINTSLICE_ENDPOINT_LB"); raw != "" {
		endpointLB, err := strconv.ParseBool(raw)
		if err != nil {
			logger.Error(err, "failed to parse 'ROUTER_ENDPOINTSLICE_ENDPOINT_LB' - endpoint LB stays off", "value", raw)
		} else {
			cfg.endpointSliceEndpointLB = endpointLB
		}
	}

	switch mode := endpointSliceCacheMode(os.Getenv("ROUTER_ENDPOINTSLICE_CACHE_MODE")); mode {
	case "", endpointSliceCacheOff:
		cfg.endpointSliceCacheMode = endpointSliceCacheOff
	case endpointSliceCacheOn:
		cfg.endpointSliceCacheMode = mode
	case "shadow":
		// The migration-era comparator was removed with the RFC-0002 phase-4
		// defaults flip; failing loud beats silently picking a side.
		return cfg, fmt.Errorf("'ROUTER_ENDPOINTSLICE_CACHE_MODE' \"shadow\" was removed with the RFC-0002 defaults flip; use \"on\" (or \"off\" for the legacy data plane)")
	default:
		return cfg, fmt.Errorf("'ROUTER_ENDPOINTSLICE_CACHE_MODE' must be one of off|on, got %q", mode)
	}

	return cfg, nil
}
