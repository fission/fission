// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	jsonv1 "encoding/json"
	"encoding/json/v2"
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

// statetokenWireOpts pins the on-disk statetoken file to exact v1 emission;
// shared with the jsonwire_compat fixture so the byte-golden gates this site.
// Cross-language SDKs parse the file. Defensive today - StateCredentials is
// three plain strings, which v2 defaults would emit identically - but the
// option keeps the contract explicit if the struct ever grows.
var statetokenWireOpts = jsonv1.DefaultOptionsV1()

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
	return writeCredentialFile(fetcher, StateTokenFileName, creds, statetokenWireOpts)
}

// writeCredentialFile marshals creds to exact-v1 JSON (the cross-language SDK
// file contract) and writes it under the shared mount as a read-only 0444 file,
// replacing any existing one: the env container runs as a different user and only
// reads it, and a re-specialize on an infinite-functions pool cannot overwrite a
// 0444 file in place. It is the shared write tail of writeStateTokenFile and
// writeAgentTokenFile — the read-only-replace step is security-sensitive (it
// closes the torn-read window on a re-specialize), so both credential kinds go
// through this one implementation rather than keeping it in lockstep by hand.
// Generic on the credential type so the caller's struct survives the call (the
// house no-any-params rule); it is marshaled by exactly the opts passed.
func writeCredentialFile[T any](fetcher *Fetcher, fileName string, creds T, opts jsonv1.Options) error {
	blob, err := json.Marshal(creds, opts)
	if err != nil {
		return err
	}
	path := filepath.Join(fetcher.sharedVolumePath, fileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, blob, 0444)
}
