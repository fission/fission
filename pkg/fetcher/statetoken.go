// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// StateTokenFileName is the file under the shared mount (/userfunc) where the
// fetcher writes the function's scoped state token at specialize time
// (RFC-0023). It is a file, not an env var, because a poolmgr generic pod's
// user container is already running before its function identity is known —
// env vars cannot be added to a running container. The executor points the
// SDK at it via FISSION_STATE_TOKEN_PATH.
const StateTokenFileName = ".fission-state-token"

// StateCredentials is the JSON document the fetcher writes to
// StateTokenFileName: everything a token-carrying client must present —
// statesvc's stateless verification re-derives from the claimed (namespace,
// keyspace), so the claims travel WITH the token.
type StateCredentials struct {
	Namespace string `json:"namespace"`
	Keyspace  string `json:"keyspace"`
	Token     string `json:"token"`
}

// writeStateTokenFile derives the RFC-0023 keyspace token from the fetcher's
// master secret and writes the StateCredentials JSON to StateTokenFileName
// under the shared mount (0444: the env container runs as a different user
// and only ever reads it). Without a master secret (dev clusters) it writes a
// placeholder token — statesvc's pass-through mode accepts any bearer, and
// the SDK contract stays uniform.
func (fetcher *Fetcher) writeStateTokenFile(loadReq FunctionLoadRequest) error {
	if loadReq.FunctionMetadata == nil {
		return errors.New("specialize request with a state keyspace but no function metadata")
	}
	creds := StateCredentials{
		Namespace: loadReq.FunctionMetadata.Namespace,
		Keyspace:  loadReq.StateKeyspace,
		Token:     "dev-unauthenticated",
	}
	if master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET")); len(master) > 0 {
		creds.Token = hmacauth.EncodeKeyForEnv(hmacauth.DeriveStateKeyspaceKey(master,
			creds.Namespace, creds.Keyspace))
	}
	blob, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	path := filepath.Join(fetcher.sharedVolumePath, StateTokenFileName)
	// The file is read-only, so a re-specialize (infinite-functions pools,
	// retried specialization) cannot overwrite in place — replace it.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, blob, 0444)
}
