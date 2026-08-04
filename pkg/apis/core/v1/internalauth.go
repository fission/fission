// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"fmt"
	"os"
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
//   - present but EMPTY: refused. This is exactly what a misconfigured
//     Secret produces — the chart's secretKeyRef injects an
//     existing-but-empty `secret` key verbatim — and every downstream
//     constructor treats an empty key as "disabled", silently demoting the
//     deployment to unauthenticated pass-through: the GHSA-3g33-6vg6-27m8
//     exposure internal auth exists to close. A MISSING key already fails
//     closed (the pod wedges in CreateContainerConfigError); the empty
//     value must not be the one misconfiguration that fails open.
//
// Validating once per process is what lets the many env readers stay
// unchanged and unable to disagree: past startup, an empty Getenv can only
// mean absent.
func ValidateInternalAuthEnv() error {
	for _, env := range []string{InternalAuthSecretEnv, InternalAuthSecretOldEnv} {
		if v, ok := os.LookupEnv(env); ok && v == "" {
			return fmt.Errorf("%s is set but empty: internal auth is configured, yet the key material is missing — refusing to run with HMAC verification silently disabled; fix the referenced Secret's key, or remove the variable to run with internal auth off", env)
		}
	}
	return nil
}

// InternalAuthSecretName is the name of the Secret holding the internal-auth
// HMAC master for this install.
//
// It exists so the name is resolved in exactly ONE place. The fetcher pod-spec
// builder and the storagesvc client both need it, and a disagreement between
// them does not fail loudly: the fetcher's secretKeyRef is marked optional, so
// the pod starts, the env var is simply absent, and every archive fetch and
// builder upload 401s with nothing naming the Secret as the cause.
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
