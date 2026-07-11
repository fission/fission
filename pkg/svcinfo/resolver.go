// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package svcinfo

import "os"

// AddressResolver yields the URLs a fission-bundle subsystem uses to reach
// its sibling services. It is the single seam for service-to-service
// addressing — mirroring the crd.ClientGeneratorInterface DI shape — so the
// three historic mechanisms (URL flags with hardcoded-namespace defaults, the
// ROUTER_INTERNAL_URL env override, and per-call Sprintf) converge in one
// place with one documented precedence.
type AddressResolver interface {
	// ExecutorURL is the executor API base URL (router → executor RPC).
	ExecutorURL() string
	// RouterURL is the router's public base URL.
	RouterURL() string
	// RouterInternalURL is where internal publishers (kubewatcher, timer,
	// mqtrigger, mqt_keda, mcp) send /fission-function/... requests — the
	// router's internal listener after the GHSA-3g33-6vg6-27m8 split.
	RouterInternalURL() string
	// StorageSvcURL is the storage service base URL (buildermgr → storagesvc).
	StorageSvcURL() string
}

// StaticResolver returns fixed URLs; the test-side implementation.
type StaticResolver struct {
	Executor, Router, RouterInternal, StorageSvc string
}

func (s StaticResolver) ExecutorURL() string       { return s.Executor }
func (s StaticResolver) RouterURL() string         { return s.Router }
func (s StaticResolver) RouterInternalURL() string { return s.RouterInternal }
func (s StaticResolver) StorageSvcURL() string     { return s.StorageSvc }

// FlagValues carries fission-bundle's URL flag values plus whether each flag
// was explicitly set on the command line (flag.Visit) — an unset flag falls
// through to the namespace-derived default rather than pinning the flag's
// compile-time default namespace.
type FlagValues struct {
	ExecutorURL, RouterURL, StorageSvcURL string
	ExecutorSet, RouterSet, StorageSvcSet bool
}

// envResolver resolves each URL with the precedence:
//
//  1. the explicitly-set command-line flag (the chart always passes these),
//  2. the service-specific env override (only ROUTER_INTERNAL_URL exists:
//     it beats --routerUrl for the publishers' target, preserving the
//     established contract),
//  3. the in-cluster default built from POD_NAMESPACE (downward API),
//     falling back to the historic "fission" namespace when unset.
type envResolver struct {
	flags     FlagValues
	namespace string
}

// NewEnvResolver builds the production resolver from the parsed flags and
// the process environment.
func NewEnvResolver(flags FlagValues) AddressResolver {
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		ns = "fission"
	}
	return envResolver{flags: flags, namespace: ns}
}

func (r envResolver) ExecutorURL() string {
	if r.flags.ExecutorSet {
		return r.flags.ExecutorURL
	}
	return ExecutorURL(r.namespace)
}

func (r envResolver) RouterURL() string {
	if r.flags.RouterSet {
		return r.flags.RouterURL
	}
	return RouterURL(r.namespace)
}

func (r envResolver) RouterInternalURL() string {
	// ROUTER_INTERNAL_URL (set by the chart on internal-publisher pods)
	// wins over --routerUrl. When neither is set the publishers keep
	// today's fallback — the PUBLIC router URL — because a non-chart
	// install without the env var may predate the listener split; the
	// chart always sets the env var.
	if internal := os.Getenv("ROUTER_INTERNAL_URL"); internal != "" {
		return internal
	}
	return r.RouterURL()
}

func (r envResolver) StorageSvcURL() string {
	if r.flags.StorageSvcSet {
		return r.flags.StorageSvcURL
	}
	return StorageSvcURL(r.namespace)
}
