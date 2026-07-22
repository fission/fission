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

// StateAPIEnvVars returns the env vars a function's USER container needs to
// reach the RFC-0023 state API, or nil when the feature is off. The split is
// deliberate (pre-implementation review pin): the statesvc URL is
// function-agnostic so it CAN be a plain env var even on a poolmgr generic
// pod (whose user container starts before its function identity is known);
// the per-function token cannot — the fetcher writes it to the shared mount
// at specialize time, and FISSION_STATE_TOKEN_PATH just tells the SDK where.
//
// statesvcURL comes from the executor's STATESVC_URL env (chart-set only when
// functionState.enabled); empty means the feature is not deployed and no vars
// are injected, keeping pods byte-identical to today.
func StateAPIEnvVars(sharedMountPath string) []apiv1.EnvVar {
	statesvcURL := os.Getenv("STATESVC_URL")
	if statesvcURL == "" {
		return nil
	}
	return []apiv1.EnvVar{
		{Name: "FISSION_STATE_URL", Value: statesvcURL},
		{Name: "FISSION_STATE_TOKEN_PATH", Value: filepath.Join(sharedMountPath, fetcher.StateTokenFileName)},
	}
}
