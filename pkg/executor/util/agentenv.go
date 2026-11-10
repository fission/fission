// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"os"
	"path/filepath"

	apiv1 "k8s.io/api/core/v1"

	"github.com/fission/fission/pkg/fetcher"
)

// AgentAPIEnvVars returns the env vars a function's USER container needs to
// reach the G16 agent runtime, or nil when the feature is off. The split is
// deliberate (mirrors StateAPIEnvVars's pre-implementation review pin): the
// agent runtime URL is function-agnostic so it CAN be a plain env var even
// on a poolmgr generic pod (whose user container starts before its function
// identity is known); the per-agent token cannot — the fetcher writes it to
// the shared mount at specialize time, and FISSION_AGENT_TOKEN_PATH just
// tells the SDK where.
//
// agentRuntimeURL comes from the executor's own AGENT_RUNTIME_URL env
// (chart-set only when agentRuntime.enabled); empty means the feature is not
// deployed and no vars are injected, keeping pods byte-identical to today.
//
// Boundary: the container executortype has no fetcher sidecar and therefore
// no call site for this function — a container-executor agent gets no
// identity file in phase 1.
func AgentAPIEnvVars(sharedMountPath string) []apiv1.EnvVar {
	agentRuntimeURL := os.Getenv("AGENT_RUNTIME_URL")
	if agentRuntimeURL == "" {
		return nil
	}
	return []apiv1.EnvVar{
		{Name: "FISSION_AGENT_RUNTIME_URL", Value: agentRuntimeURL},
		{Name: "FISSION_AGENT_TOKEN_PATH", Value: filepath.Join(sharedMountPath, fetcher.AgentTokenFileName)},
	}
}

// SidecarAPIEnvVars aggregates the env-var groups a function's user container
// needs for every fetcher-sidecar-delivered API (state, then agent identity),
// each nil when its feature is off. It is the single aggregation seam for these
// groups: a future sidecar-delivered API is added here once, rather than
// deepening an append(append(...)) nest at every executor call site.
func SidecarAPIEnvVars(sharedMountPath string) []apiv1.EnvVar {
	return append(StateAPIEnvVars(sharedMountPath), AgentAPIEnvVars(sharedMountPath)...)
}
