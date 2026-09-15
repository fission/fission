// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	jsonv1 "encoding/json"
	"errors"
	"os"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// AgentTokenFileName is the file under the shared mount (/userfunc) where
// the fetcher writes the function's per-agent identity token at specialize
// time (G16). It is a file, not an env var, because a poolmgr generic pod's
// user container is already running before its function identity is known —
// env vars cannot be added to a running container. The executor points the
// SDK at it via FISSION_AGENT_TOKEN_PATH.
const AgentTokenFileName = ".fission-agent-token"

// AgentCredentials is the JSON document the fetcher writes to
// AgentTokenFileName: everything a token-carrying client must present —
// agentruntime's stateless verification re-derives from the claimed
// (namespace, agent), so the claims travel WITH the token.
type AgentCredentials struct {
	Namespace string `json:"namespace"`
	Agent     string `json:"agent"`
	Token     string `json:"token"`
}

// agenttokenWireOpts pins the on-disk agenttoken file to exact v1 emission;
// see statetokenWireOpts for why this matters even though AgentCredentials
// is three plain strings that v2 defaults would emit identically today.
var agenttokenWireOpts = jsonv1.DefaultOptionsV1()

// writeAgentTokenFile derives the G16 agent identity token from the
// fetcher's master secret and writes the AgentCredentials JSON to
// AgentTokenFileName under the shared mount (0444: the env container runs
// as a different user and only ever reads it). Without a master secret (dev
// clusters) it writes a placeholder token — agentruntime's pass-through mode
// accepts any bearer, and the SDK contract stays uniform.
func (fetcher *Fetcher) writeAgentTokenFile(loadReq FunctionLoadRequest) error {
	if loadReq.FunctionMetadata == nil {
		return errors.New("specialize request with an agent name but no function metadata")
	}
	creds := AgentCredentials{
		Namespace: loadReq.FunctionMetadata.Namespace,
		Agent:     loadReq.AgentName,
		Token:     "dev-unauthenticated",
	}
	if master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET")); len(master) > 0 {
		creds.Token = hmacauth.EncodeKeyForEnv(hmacauth.DeriveAgentIdentityKey(master,
			creds.Namespace, creds.Agent))
	}
	return writeCredentialFile(fetcher, AgentTokenFileName, creds, agenttokenWireOpts)
}
