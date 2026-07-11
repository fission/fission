// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func routerPod(ns, name string) *v1.Pod {
	return &v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns, Labels: map[string]string{"application": "fission-router"},
	}}
}

func routerSvc(ns string, ports ...v1.ServicePort) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "router", Namespace: ns, Labels: map[string]string{"application": "fission-router"},
		},
		Spec: v1.ServiceSpec{Ports: ports},
	}
}

// TestResolveFissionService pins the CLI's install-discovery UX: single
// install resolves; several installs error with the FISSION_NAMESPACE hint;
// the Service's LAST targetPort wins (historic behavior).
func TestResolveFissionService(t *testing.T) {
	t.Parallel()
	const selector = "application=fission-router"

	t.Run("single install resolves", func(t *testing.T) {
		t.Parallel()
		kube := fake.NewClientset(routerPod("fission", "router-1"),
			routerSvc("fission", v1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8888)}))
		ns, tp, err := resolveFissionService(t.Context(), kube, "", selector)
		require.NoError(t, err)
		assert.Equal(t, "fission", ns)
		assert.Equal(t, intstr.FromInt(8888), tp)
	})

	t.Run("two installs error with namespace hint", func(t *testing.T) {
		t.Parallel()
		kube := fake.NewClientset(routerPod("fission", "router-1"), routerPod("fission2", "router-2"))
		_, _, err := resolveFissionService(t.Context(), kube, "", selector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "found 2 fission installs")
		assert.Contains(t, err.Error(), "FISSION_NAMESPACE")
	})

	t.Run("explicit namespace scopes the search", func(t *testing.T) {
		t.Parallel()
		kube := fake.NewClientset(routerPod("fission", "router-1"), routerPod("fission2", "router-2"),
			routerSvc("fission2", v1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8888)}))
		ns, _, err := resolveFissionService(t.Context(), kube, "fission2", selector)
		require.NoError(t, err)
		assert.Equal(t, "fission2", ns)
	})

	t.Run("last service port wins", func(t *testing.T) {
		t.Parallel()
		kube := fake.NewClientset(routerPod("fission", "router-1"),
			routerSvc("fission",
				v1.ServicePort{Name: "a", Port: 80, TargetPort: intstr.FromInt(8888)},
				v1.ServicePort{Name: "b", Port: 81, TargetPort: intstr.FromString("http")}))
		_, tp, err := resolveFissionService(t.Context(), kube, "", selector)
		require.NoError(t, err)
		assert.Equal(t, intstr.FromString("http"), tp)
	})

	t.Run("no pods", func(t *testing.T) {
		t.Parallel()
		_, _, err := resolveFissionService(t.Context(), fake.NewClientset(), "", selector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no available pod")
	})

	t.Run("no service", func(t *testing.T) {
		t.Parallel()
		kube := fake.NewClientset(routerPod("fission", "router-1"))
		_, _, err := resolveFissionService(t.Context(), kube, "", selector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// TestBridgeToRoute pins the local bridge: plain HTTP clients dial the
// returned 127.0.0.1 port and reach the route's backend through the registry
// — including a bare POST (the token-create shape) and sequential
// connections through the persistent accept loop.
func TestBridgeToRoute(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	reg := portless.New()
	t.Cleanup(func() { _ = reg.Close() })
	b, err := backend.ParseTCP(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	_, err = reg.Add(t.Context(), "bridge-test", b)
	require.NoError(t, err)

	port, err := bridgeToRoute(reg, "bridge-test")
	require.NoError(t, err)
	base := "http://127.0.0.1:" + port

	resp, err := http.Get(base + "/one")
	require.NoError(t, err)
	assert.Equal(t, "GET /one", readAll(t, resp))

	resp, err = http.Post(base+"/auth/login", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	assert.Equal(t, "POST /auth/login", readAll(t, resp))
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
