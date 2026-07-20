// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anishathalye/porcupine"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// RFC-0023 S2 (no lost updates), checked against the real HTTP surface — not
// just the driver: concurrent get→CAS clients through statesvc must form a
// linearizable history, and a get→cas counter loop must lose zero increments.
// The per-key CAS protocol itself is the substrate's already-TLC-covered
// Set+IfVersion; this pins statesvc's plumbing of it (If-Match, /cas, version
// headers) end to end.

type httpRegInput struct {
	op       string // "get" | "cas"
	expected int64
	newVal   int
}

type httpRegOutput struct {
	ver int64
	val int
	ok  bool
}

type httpRegState struct {
	ver int64
	val int
}

var httpRegisterModel = porcupine.Model{
	Init: func() any { return httpRegState{ver: 1, val: 0} },
	Step: func(st, in, out any) (bool, any) {
		s := st.(httpRegState)
		i := in.(httpRegInput)
		o := out.(httpRegOutput)
		switch i.op {
		case "get":
			return o.ver == s.ver && o.val == s.val, s
		case "cas":
			if i.expected == s.ver {
				if !o.ok {
					return false, s
				}
				return true, httpRegState{ver: s.ver + 1, val: i.newVal}
			}
			return !o.ok, s
		default:
			return false, s
		}
	},
	Equal: func(a, b any) bool { return a.(httpRegState) == b.(httpRegState) },
}

type stateClient struct {
	srv   *httptest.Server
	token string
	ns    string
	ks    string
}

func (c *stateClient) get(t *testing.T, key string) (int64, []byte, int) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.srv.URL+"/v1/state/"+key, nil)
	require.NoError(t, err)
	c.decorate(req)
	resp, err := c.srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	ver, _ := strconv.ParseInt(resp.Header.Get(HeaderStateVersion), 10, 64)
	return ver, body, resp.StatusCode
}

func (c *stateClient) cas(t *testing.T, key string, expect int64, val []byte) int {
	t.Helper()
	body, err := json.Marshal(casRequest{ExpectVersion: expect, Value: val})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.srv.URL+"/v1/state/"+key+"/cas", bytes.NewReader(body))
	require.NoError(t, err)
	c.decorate(req)
	resp, err := c.srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func (c *stateClient) decorate(req *http.Request) {
	req.Header.Set(HeaderStateNamespace, c.ns)
	req.Header.Set(HeaderStateKeyspace, c.ks)
	req.Header.Set("Authorization", "Bearer "+c.token)
}

func TestStateAPILinearizable(t *testing.T) {
	srv, _ := newTestServer(t, map[types.NamespacedName]*fv1.StateConfig{fnA: {}})
	client := &stateClient{srv: srv, token: stateToken("ns-a", "fn-a"), ns: "ns-a", ks: "fn-a"}

	// Seed version 1 value 0.
	require.Equal(t, http.StatusNoContent, client.cas(t, "reg", 0, []byte("0")))

	const clients, opsPerClient = 6, 30
	var (
		clock atomic.Int64
		mu    sync.Mutex
		ops   []porcupine.Operation
		wg    sync.WaitGroup
	)
	record := func(id int, in httpRegInput, call int64, out httpRegOutput, ret int64) {
		mu.Lock()
		defer mu.Unlock()
		ops = append(ops, porcupine.Operation{ClientId: id, Input: in, Call: call, Output: out, Return: ret})
	}

	for id := range clients {
		wg.Go(func() {
			lastVer, lastVal := int64(1), 0
			for op := range opsPerClient {
				if op%2 == 0 { // read
					call := clock.Add(1)
					ver, body, code := client.get(t, "reg")
					ret := clock.Add(1)
					if code != http.StatusOK {
						continue
					}
					val, _ := strconv.Atoi(string(body))
					lastVer, lastVal = ver, val
					record(id, httpRegInput{op: "get"}, call, httpRegOutput{ver: ver, val: val}, ret)
				} else { // CAS on the last observed version
					newVal := lastVal + id + 1
					call := clock.Add(1)
					code := client.cas(t, "reg", lastVer, []byte(strconv.Itoa(newVal)))
					ret := clock.Add(1)
					record(id, httpRegInput{op: "cas", expected: lastVer, newVal: newVal}, call,
						httpRegOutput{ok: code == http.StatusNoContent}, ret)
				}
			}
		})
	}
	wg.Wait()

	require.True(t, porcupine.CheckOperations(httpRegisterModel, ops),
		"statesvc CAS history is not linearizable (S2)")
}

// TestStateAPICounterNoLostIncrements is the classic get→cas counter under
// concurrent load: every client retries its CAS until it wins once; the final
// value must equal the number of clients (zero lost updates).
func TestStateAPICounterNoLostIncrements(t *testing.T) {
	srv, _ := newTestServer(t, map[types.NamespacedName]*fv1.StateConfig{fnA: {}})
	client := &stateClient{srv: srv, token: stateToken("ns-a", "fn-a"), ns: "ns-a", ks: "fn-a"}

	require.Equal(t, http.StatusNoContent, client.cas(t, "counter", 0, []byte("0")))

	const writers = 24
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for {
				ver, body, code := client.get(t, "counter")
				if code != http.StatusOK {
					continue
				}
				n, _ := strconv.Atoi(string(body))
				if client.cas(t, "counter", ver, []byte(strconv.Itoa(n+1))) == http.StatusNoContent {
					return
				}
			}
		})
	}
	wg.Wait()

	_, body, code := client.get(t, "counter")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, strconv.Itoa(writers), string(body), "lost increments detected (S2)")
}
