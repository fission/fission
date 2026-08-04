// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"fmt"
	"os"
	"strings"
)

// ValidateInternalAuthEnv enforces the fail-closed contract on the
// internal-auth environment. It is called once from every binary's entry
// point (fission-bundle — covering all its subsystems — fetcher, builder,
// and the CLI) before anything constructs a signer or verifier.
//
// Three states, two of them fine:
//   - variable ABSENT: internal auth is disabled; signers and verifiers pass
//     through, the documented dev-mode contract.
//   - present and NON-EMPTY: internal auth is on.
//   - present but blank (empty OR whitespace-only): refused. An empty value is
//     exactly what a misconfigured Secret produces — the chart's secretKeyRef
//     injects an existing-but-empty `secret` key verbatim — and every
//     downstream constructor treats a blank key as "disabled", silently
//     demoting the deployment to unauthenticated pass-through: the
//     GHSA-3g33-6vg6-27m8 exposure internal auth exists to close.
//
// On the chart's CONTROL-PLANE Deployments a MISSING key already fails closed
// (the secretKeyRef is non-optional, so the pod wedges in
// CreateContainerConfigError) — the blank value was the one that failed open.
// Note this does NOT reach the fetcher/builder sidecars, whose secretKeyRef is
// `optional: true`: a missing key there yields ABSENT, which is the documented
// auth-disabled state. Closing that (a renamed data key on an existingSecret
// leaving function-pod verifiers in pass-through) needs a non-optional mount
// or a data-key check, tracked separately.
//
// Validating once per process is what lets the many env readers stay unchanged
// and unable to disagree: past startup, in every shipped binary, an empty
// Getenv can only mean absent. (In-process test harnesses bypass main() and
// may set these vars mid-process; they own their own state.)
func ValidateInternalAuthEnv() error {
	for _, env := range []string{InternalAuthSecretEnv, InternalAuthSecretOldEnv} {
		if v, ok := os.LookupEnv(env); ok && strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is set but blank: internal auth is configured, yet the key material is missing — refusing to run with HMAC verification silently disabled; fix the referenced Secret's key, or remove the variable to run with internal auth off", env)
		}
	}
	return nil
}

// InternalAuthSecretName is the name of the Secret holding the internal-auth
// HMAC master for this install.
//
// It is the resolver for IN-CLUSTER components (the fetcher pod-spec builder,
// the storagesvc server), which read the name from their own process env. A
// disagreement between them does not fail loudly: the fetcher's secretKeyRef
// is marked optional, so the pod starts, the env var is simply absent, and
// every archive fetch and builder upload 401s with nothing naming the Secret
// as the cause.
//
// Off-cluster callers (the CLI) have no such env and resolve the name from the
// running cluster instead — see internalAuthSecretNameFromCluster in
// pkg/storagesvc/client, whose precedence is a strict superset of this one
// (this function's env-or-default is its tiers 1 and 3).
//
// The default is the chart-generated name. internalAuth.existingSecret points
// an install at a pre-created Secret instead — the supported path for GitOps
// renderers, where the chart cannot preserve a generated value across a
// `helm template` sync.
func InternalAuthSecretName() string {
	if name := os.Getenv(InternalAuthSecretNameEnv); name != "" {
		return name
	}
	return DefaultInternalAuthSecret
}
