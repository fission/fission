// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// fakeDLQInput embeds cli.Input (nil) and overrides only the accessors the DLQ
// actioners use.
type fakeDLQInput struct {
	cli.Input
	s   map[string]string
	b   map[string]bool
	i   map[string]int
	set map[string]bool
}

func (f fakeDLQInput) String(k string) string   { return f.s[k] }
func (f fakeDLQInput) Bool(k string) bool       { return f.b[k] }
func (f fakeDLQInput) Int(k string) int         { return f.i[k] }
func (f fakeDLQInput) IsSet(k string) bool      { return f.set[k] }
func (f fakeDLQInput) Context() context.Context { return context.Background() }

// recordedReq captures one request the mock router received.
type recordedReq struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   string
}

// mockRouter starts an httptest.Server standing in for the router DLQ API, points
// FISSION_ROUTER_URL at it, and records every request. handler returns the JSON
// body per path.
func mockRouter(t *testing.T, handler func(r *http.Request) (int, string)) *[]recordedReq {
	t.Helper()
	var got []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, recordedReq{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Auth: r.Header.Get("Authorization"), Body: string(body),
		})
		code, resp := handler(r)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FISSION_ROUTER_URL", srv.URL)
	return &got
}

func TestDLQCLIList(t *testing.T) {
	got := mockRouter(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"messages":[{"id":"asyncinv/1","namespace":"ns","function":"fn","reason":"http_4xx","attempts":3}]}`
	})
	t.Setenv("FISSION_AUTH_TOKEN", "tok")
	in := fakeDLQInput{
		s:   map[string]string{flagkey.Namespace: "ns"},
		i:   map[string]int{flagkey.DlqLimit: 50},
		set: map[string]bool{flagkey.DlqLimit: true},
	}
	require.NoError(t, (&dlqSubCommand{}).list(in))

	require.Len(t, *got, 1)
	req := (*got)[0]
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, dlqAPIList, req.Path)
	assert.Contains(t, req.Query, "namespace=ns")
	assert.Contains(t, req.Query, "limit=50")
	assert.Equal(t, "Bearer tok", req.Auth, "the auth token is sent")
}

func TestDLQCLIShow(t *testing.T) {
	got := mockRouter(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"id":"asyncinv/7","namespace":"ns","function":"fn","envelope":{"function":"fn"}}`
	})
	in := fakeDLQInput{s: map[string]string{flagkey.DlqID: "asyncinv/7"}}
	require.NoError(t, (&dlqSubCommand{}).show(in))

	require.Len(t, *got, 1)
	assert.Equal(t, dlqAPIShow, (*got)[0].Path)
	assert.Contains(t, (*got)[0].Query, "id=asyncinv%2F7")
}

func TestDLQCLIRedriveByID(t *testing.T) {
	got := mockRouter(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"count":1}`
	})
	in := fakeDLQInput{s: map[string]string{flagkey.DlqID: "asyncinv/3"}}
	require.NoError(t, (&dlqSubCommand{}).redrive(in))

	require.Len(t, *got, 1)
	req := (*got)[0]
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, dlqAPIRedrive, req.Path)
	var body dlqRedriveReq
	require.NoError(t, json.Unmarshal([]byte(req.Body), &body))
	assert.Equal(t, []string{"asyncinv/3"}, body.IDs)
}

func TestDLQCLIRedriveAll(t *testing.T) {
	got := mockRouter(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			return http.StatusOK, `{"messages":[{"id":"a"},{"id":"b"}]}`
		}
		return http.StatusOK, `{"count":2}`
	})
	in := fakeDLQInput{b: map[string]bool{flagkey.DlqAll: true}}
	require.NoError(t, (&dlqSubCommand{}).redrive(in))

	require.Len(t, *got, 2, "redrive --all lists then redrives")
	assert.Equal(t, http.MethodGet, (*got)[0].Method)
	assert.Equal(t, http.MethodPost, (*got)[1].Method)
	var body dlqRedriveReq
	require.NoError(t, json.Unmarshal([]byte((*got)[1].Body), &body))
	assert.Equal(t, []string{"a", "b"}, body.IDs)
}

func TestDLQCLIRedriveArgErrors(t *testing.T) {
	mockRouter(t, func(*http.Request) (int, string) { return http.StatusOK, `{}` })
	// Neither --id nor --all.
	err := (&dlqSubCommand{}).redrive(fakeDLQInput{})
	assert.ErrorContains(t, err, "one of --id or --all")
	// Both.
	err = (&dlqSubCommand{}).redrive(fakeDLQInput{
		s: map[string]string{flagkey.DlqID: "x"}, b: map[string]bool{flagkey.DlqAll: true},
	})
	assert.ErrorContains(t, err, "mutually exclusive")
}

func TestDLQCLIPurge(t *testing.T) {
	got := mockRouter(t, func(*http.Request) (int, string) { return http.StatusOK, `{"count":5}` })
	require.NoError(t, (&dlqSubCommand{}).purge(fakeDLQInput{}))
	require.Len(t, *got, 1)
	assert.Equal(t, http.MethodPost, (*got)[0].Method)
	assert.Equal(t, dlqAPIPurge, (*got)[0].Path)
}

func TestDLQCLIDisabled501(t *testing.T) {
	mockRouter(t, func(*http.Request) (int, string) {
		return http.StatusNotImplemented, "async invocation is not enabled on this cluster\n"
	})
	err := (&dlqSubCommand{}).purge(fakeDLQInput{})
	assert.ErrorContains(t, err, "not enabled")
}

func TestDLQCLIServerError(t *testing.T) {
	mockRouter(t, func(*http.Request) (int, string) {
		return http.StatusInternalServerError, "boom\n"
	})
	err := (&dlqSubCommand{}).purge(fakeDLQInput{})
	assert.ErrorContains(t, err, "500")
	assert.ErrorContains(t, err, "boom")
}
