// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import "os"

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
