// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"testing"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// FuzzStateTokenVerify is RFC-0023 S1's adversary: nothing but the exact
// derived token for exactly the claimed (namespace, keyspace) may
// authenticate. The fuzzer mutates the token (bit flips, truncation,
// re-encoding) and splices scope fields; any acceptance that is not the
// precise re-derivation is a scope-isolation break.
func FuzzStateTokenVerify(f *testing.F) {
	master := []byte("fuzz-master-secret")
	masterOld := []byte("fuzz-master-old")
	a := newAuthenticator(master, masterOld, hmacauth.VerifierOpts{})

	valid := stateTokenFor(master, "ns-a", "cart")
	f.Add(valid, "ns-a", "cart")
	f.Add(valid, "ns-b", "cart")                              // namespace splice
	f.Add(valid, "ns-a", "sessions")                          // keyspace splice
	f.Add(valid[:len(valid)-2], "ns-a", "cart")               // truncation
	f.Add(valid+"00", "ns-a", "cart")                         // extension
	f.Add(stateTokenFor(masterOld, "ns-a", "cart"), "ns-a", "cart") // rotation key (valid)
	f.Add("", "ns-a", "cart")
	f.Add("not-hex-at-all", "ns-a", "cart")

	f.Fuzz(func(t *testing.T, token, ns, keyspace string) {
		got := a.verifyKeyspaceToken(token, ns, keyspace)
		want := token == stateTokenFor(master, ns, keyspace) || token == stateTokenFor(masterOld, ns, keyspace)
		if got != want {
			t.Fatalf("verifyKeyspaceToken(%q, %q, %q) = %v, want %v", token, ns, keyspace, got, want)
		}
	})
}

func stateTokenFor(master []byte, ns, keyspace string) string {
	return hmacauth.EncodeKeyForEnv(hmacauth.DeriveStateKeyspaceKey(master, ns, keyspace))
}
