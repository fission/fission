// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package token

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	runErr := fn()
	w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	require.NoError(t, runErr)
	return buf.String()
}

func TestTokenCreate(t *testing.T) {
	t.Run("prints access token on 201", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/auth/login", r.URL.Path)
			var body fv1.AuthLogin
			_ = json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "alice", body.Username)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fv1.RouterAuthToken{AccessToken: "tok-123", TokenType: "Bearer"})
		}))
		t.Cleanup(srv.Close)
		t.Setenv("FISSION_ROUTER_URL", srv.URL)

		in := dummy.TestFlagSet()
		in.Set(flagkey.TokUsername, "alice")
		in.Set(flagkey.TokPassword, "secret")

		out := captureStdout(t, func() error { return Create(in) })
		assert.Contains(t, out, "tok-123")
	})

	t.Run("reports a 404 with guidance", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("FISSION_ROUTER_URL", srv.URL)

		in := dummy.TestFlagSet()
		in.Set(flagkey.TokUsername, "bob")
		in.Set(flagkey.TokPassword, "secret")

		out := captureStdout(t, func() error { return Create(in) })
		assert.True(t, strings.Contains(out, "authentication is enabled"),
			"404 should print guidance, got: %q", out)
	})

	t.Run("honors the --authuri flag", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fv1.RouterAuthToken{AccessToken: "x"})
		}))
		t.Cleanup(srv.Close)
		t.Setenv("FISSION_ROUTER_URL", srv.URL)

		in := dummy.TestFlagSet()
		in.Set(flagkey.TokUsername, "alice")
		in.Set(flagkey.TokPassword, "secret")
		in.Set(flagkey.TokAuthURI, "/custom/login")

		_ = captureStdout(t, func() error { return Create(in) })
		assert.Equal(t, "/custom/login", gotPath)
	})
}
