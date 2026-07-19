// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/info"
)

func TestGetResourceReqs(t *testing.T) {
	t.Parallel()

	mustQty := func(s string) resource.Quantity { return resource.MustParse(s) }

	tests := []struct {
		name      string
		flags     map[string]any
		existing  *v1.ResourceRequirements
		wantErr   bool
		wantReqs  map[v1.ResourceName]resource.Quantity
		wantLimit map[v1.ResourceName]resource.Quantity
	}{
		{
			name:      "no flags yields empty maps",
			flags:     map[string]any{},
			wantReqs:  map[v1.ResourceName]resource.Quantity{},
			wantLimit: map[v1.ResourceName]resource.Quantity{},
		},
		{
			name:      "mincpu only defaults limit to request",
			flags:     map[string]any{flagkey.RuntimeMincpu: 100},
			wantReqs:  map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: mustQty("100m")},
			wantLimit: map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: mustQty("100m")},
		},
		{
			name:      "minmemory only defaults limit to request",
			flags:     map[string]any{flagkey.RuntimeMinmemory: 64},
			wantReqs:  map[v1.ResourceName]resource.Quantity{v1.ResourceMemory: mustQty("64Mi")},
			wantLimit: map[v1.ResourceName]resource.Quantity{v1.ResourceMemory: mustQty("64Mi")},
		},
		{
			name: "valid cpu and memory ranges",
			flags: map[string]any{
				flagkey.RuntimeMincpu:    100,
				flagkey.RuntimeMaxcpu:    200,
				flagkey.RuntimeMinmemory: 64,
				flagkey.RuntimeMaxmemory: 128,
			},
			wantReqs:  map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: mustQty("100m"), v1.ResourceMemory: mustQty("64Mi")},
			wantLimit: map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: mustQty("200m"), v1.ResourceMemory: mustQty("128Mi")},
		},
		{
			name:    "mincpu greater than maxcpu errors",
			flags:   map[string]any{flagkey.RuntimeMincpu: 200, flagkey.RuntimeMaxcpu: 100},
			wantErr: true,
		},
		{
			name:    "minmemory greater than maxmemory errors",
			flags:   map[string]any{flagkey.RuntimeMinmemory: 128, flagkey.RuntimeMaxmemory: 64},
			wantErr: true,
		},
		{
			name:      "existing requirements are preserved",
			flags:     map[string]any{},
			existing:  &v1.ResourceRequirements{Requests: v1.ResourceList{v1.ResourceCPU: mustQty("50m")}, Limits: v1.ResourceList{v1.ResourceCPU: mustQty("50m")}},
			wantReqs:  map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: mustQty("50m")},
			wantLimit: map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: mustQty("50m")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := dummy.TestFlagSet()
			for k, v := range tt.flags {
				in.Set(k, v)
			}
			got, err := GetResourceReqs(in, tt.existing)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			for name, want := range tt.wantReqs {
				assert.Equal(t, 0, want.Cmp(got.Requests[name]), "request %s", name)
			}
			for name, want := range tt.wantLimit {
				assert.Equal(t, 0, want.Cmp(got.Limits[name]), "limit %s", name)
			}
		})
	}
}

func TestParseAnnotations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      []string
		want    map[string]string
		wantErr bool
	}{
		{"valid pairs", []string{"a=b", "c=d"}, map[string]string{"a": "b", "c": "d"}, false},
		{"value with equals", []string{"a=b=c"}, map[string]string{"a": "b=c"}, false},
		{"empty input", nil, map[string]string{}, false},
		{"missing equals", []string{"a"}, nil, true},
		{"leading equals", []string{"=b"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAnnotations(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestApplyLabelsAndAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("applies labels and annotations", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.Set(flagkey.Labels, "team=a,env=prod")
		in.Set(flagkey.Annotation, []string{"owner=me"})
		om := &metav1.ObjectMeta{}
		require.NoError(t, ApplyLabelsAndAnnotations(in, om))
		assert.Equal(t, map[string]string{"team": "a", "env": "prod"}, om.Labels)
		assert.Equal(t, map[string]string{"owner": "me"}, om.Annotations)
	})

	t.Run("no flags leaves meta untouched", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		om := &metav1.ObjectMeta{}
		require.NoError(t, ApplyLabelsAndAnnotations(in, om))
		assert.Nil(t, om.Labels)
		assert.Nil(t, om.Annotations)
	})

	t.Run("invalid label errors", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.Set(flagkey.Labels, "not a label")
		require.Error(t, ApplyLabelsAndAnnotations(in, &metav1.ObjectMeta{}))
	})

	t.Run("invalid annotation errors", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.Set(flagkey.Annotation, []string{"noequals"})
		require.Error(t, ApplyLabelsAndAnnotations(in, &metav1.ObjectMeta{}))
	})
}

func TestGetSpecDir(t *testing.T) {
	t.Parallel()
	in := dummy.TestFlagSet()
	assert.Equal(t, "specs", GetSpecDir(in))
	in.Set(flagkey.SpecDir, "custom")
	assert.Equal(t, "custom", GetSpecDir(in))
}

func TestGetSpecIgnore(t *testing.T) {
	t.Parallel()
	in := dummy.TestFlagSet()
	assert.Equal(t, SPEC_IGNORE_FILE, GetSpecIgnore(in))
	in.Set(flagkey.SpecIgnore, ".myignore")
	assert.Equal(t, ".myignore", GetSpecIgnore(in))
}

func TestGetValidationFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		val  string
		want bool
	}{
		{"", true},
		{"false", false},
		{"true", true},
		{"anything", true},
	}
	for _, tt := range tests {
		in := dummy.TestFlagSet()
		if tt.val != "" {
			in.Set(flagkey.SpecValidate, tt.val)
		}
		assert.Equal(t, tt.want, GetValidationFlag(in), "val=%q", tt.val)
	}
}

func TestGetSpecIgnoreParser(t *testing.T) {
	t.Parallel()

	t.Run("default ignore missing returns empty parser", func(t *testing.T) {
		t.Parallel()
		p, err := GetSpecIgnoreParser(t.TempDir(), SPEC_IGNORE_FILE)
		require.NoError(t, err)
		require.NotNil(t, p)
	})

	t.Run("custom ignore missing errors", func(t *testing.T) {
		t.Parallel()
		_, err := GetSpecIgnoreParser(t.TempDir(), ".custom-ignore")
		require.Error(t, err)
	})

	t.Run("existing ignore file is parsed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, SPEC_IGNORE_FILE), []byte("*.log\n"), 0600))
		p, err := GetSpecIgnoreParser(dir, SPEC_IGNORE_FILE)
		require.NoError(t, err)
		assert.True(t, p.MatchesPath("foo.log"))
		assert.False(t, p.MatchesPath("foo.txt"))
	})
}

func TestCheckFunctionExistence(t *testing.T) {
	t.Parallel()
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "exists", Namespace: "default"}}
	client := cmd.Client{FissionClientSet: fissionfake.NewClientset(fn)}

	require.NoError(t, CheckFunctionExistence(t.Context(), client, []string{"exists"}, "default"))

	err := CheckFunctionExistence(t.Context(), client, []string{"exists", "missing"}, "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestCheckHTTPTriggerDuplicates(t *testing.T) {
	t.Parallel()
	existing := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "default"},
		Spec:       fv1.HTTPTriggerSpec{RelativeURL: "/foo", Method: http.MethodGet},
	}
	client := cmd.Client{FissionClientSet: fissionfake.NewClientset(existing)}

	t.Run("same host url method is a duplicate", func(t *testing.T) {
		dup := &fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: "t2", Namespace: "default"},
			Spec:       fv1.HTTPTriggerSpec{RelativeURL: "/foo", Method: http.MethodGet},
		}
		require.Error(t, CheckHTTPTriggerDuplicates(t.Context(), client, dup))
	})

	t.Run("different url is not a duplicate", func(t *testing.T) {
		ok := &fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: "t3", Namespace: "default"},
			Spec:       fv1.HTTPTriggerSpec{RelativeURL: "/bar", Method: http.MethodGet},
		}
		require.NoError(t, CheckHTTPTriggerDuplicates(t.Context(), client, ok))
	})

	t.Run("same resource is skipped", func(t *testing.T) {
		require.NoError(t, CheckHTTPTriggerDuplicates(t.Context(), client, existing))
	})
}

func TestSecretAndConfigMapExists(t *testing.T) {
	t.Parallel()
	secret := &v1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}
	cm := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"}}
	kc := k8sfake.NewClientset(secret, cm)

	require.NoError(t, SecretExists(t.Context(), &metav1.ObjectMeta{Name: "s", Namespace: "default"}, kc))
	require.Error(t, SecretExists(t.Context(), &metav1.ObjectMeta{Name: "nope", Namespace: "default"}, kc))

	require.NoError(t, ConfigMapExists(t.Context(), &metav1.ObjectMeta{Name: "c", Namespace: "default"}, kc))
	require.Error(t, ConfigMapExists(t.Context(), &metav1.ObjectMeta{Name: "nope", Namespace: "default"}, kc))
}

func TestGetSvcName(t *testing.T) {
	t.Parallel()

	t.Run("single matching service", func(t *testing.T) {
		t.Parallel()
		svc := &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: "fission", Labels: map[string]string{"application": "fission-router"}}}
		kc := k8sfake.NewClientset(svc)
		name, err := GetSvcName(t.Context(), kc, "fission-router")
		require.NoError(t, err)
		assert.Equal(t, "router.fission", name)
	})

	t.Run("no matching service errors", func(t *testing.T) {
		t.Parallel()
		kc := k8sfake.NewClientset()
		_, err := GetSvcName(t.Context(), kc, "fission-router")
		require.Error(t, err)
	})

	t.Run("multiple matching services error", func(t *testing.T) {
		t.Parallel()
		s1 := &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "fission", Labels: map[string]string{"application": "fission-router"}}}
		s2 := &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "fission", Labels: map[string]string{"application": "fission-router"}}}
		kc := k8sfake.NewClientset(s1, s2)
		_, err := GetSvcName(t.Context(), kc, "fission-router")
		require.Error(t, err)
	})
}

func TestGetRouterURL(t *testing.T) {
	t.Setenv("FISSION_ROUTER_URL", "http://router.example:8080")
	u, err := GetRouterURL(t.Context(), cmd.Client{})
	require.NoError(t, err)
	assert.Equal(t, "http://router.example:8080", u.String())
}

func TestGetApplicationUrl(t *testing.T) {
	t.Setenv(ENV_FISSION_URL, "http://fission.example")
	u, err := GetApplicationUrl(t.Context(), cmd.Client{}, "application=fission-router")
	require.NoError(t, err)
	assert.Equal(t, "http://fission.example", u)
}

func TestGetStorageURL(t *testing.T) {
	t.Setenv("FISSION_STORAGESVC_URL", "http://storage.example:8000")
	u, err := GetStorageURL(t.Context(), cmd.Client{})
	require.NoError(t, err)
	assert.Equal(t, "http://storage.example:8000", u.String())
}

func TestGetServerInfo(t *testing.T) {
	t.Run("decodes server info on 200", func(t *testing.T) {
		want := info.ServerInfo{Build: info.BuildMeta{Version: "9.9.9"}}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/_version", r.URL.Path)
			_ = json.NewEncoder(w).Encode(want)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("FISSION_ROUTER_URL", srv.URL)

		in := dummy.TestFlagSet()
		got := GetServerInfo(in, cmd.Client{})
		require.NotNil(t, got)
		assert.Equal(t, "9.9.9", got.Build.Version)
	})

	t.Run("non-200 yields empty server info", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("FISSION_ROUTER_URL", srv.URL)

		in := dummy.TestFlagSet()
		got := GetServerInfo(in, cmd.Client{})
		require.NotNil(t, got)
		assert.Empty(t, got.Build.Version)
	})
}

func TestGetVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(info.ServerInfo{Build: info.BuildMeta{Version: "server-1"}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FISSION_ROUTER_URL", srv.URL)

	in := dummy.TestFlagSet()
	versions := GetVersion(t.Context(), in, cmd.Client{})
	assert.Contains(t, versions.Client, "fission/core")
	assert.Equal(t, "server-1", versions.Server["fission/core"].Version)
}

func TestFunctionPodLogs(t *testing.T) {
	t.Parallel()

	t.Run("function not found errors", func(t *testing.T) {
		t.Parallel()
		client := cmd.Client{
			FissionClientSet: fissionfake.NewClientset(),
			KubernetesClient: k8sfake.NewClientset(),
		}
		require.Error(t, FunctionPodLogs(t.Context(), "missing", "default", client))
	})

	t.Run("no pods for function errors", func(t *testing.T) {
		t.Parallel()
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "uid-1"},
			Spec:       fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs", Namespace: "default"}},
		}
		client := cmd.Client{
			FissionClientSet: fissionfake.NewClientset(fn),
			KubernetesClient: k8sfake.NewClientset(),
		}
		err := FunctionPodLogs(t.Context(), "fn", "default", client)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no active pods")
	})
}

// TestGetRouterInternalURLIgnoresPublicRouterVar locks in that the internal
// listener's resolution never falls back to FISSION_ROUTER_URL (the public
// listener's override): the two are unrelated env vars, and a fallback here
// would silently point `fn test`/`fn test --async` HMAC-signed requests at
// the wrong (public) listener. With FISSION_ROUTER_INTERNAL_URL unset and no
// cluster to port-forward to, resolution must fail on pod discovery, not
// quietly succeed with the public URL.
func TestGetRouterInternalURLIgnoresPublicRouterVar(t *testing.T) {
	t.Setenv("FISSION_ROUTER_URL", "http://should-not-be-used.invalid")
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", "")

	client := cmd.Client{KubernetesClient: k8sfake.NewClientset()}
	_, err := GetRouterInternalURL(t.Context(), client)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "should-not-be-used")
	assert.Contains(t, err.Error(), "no available pod")
}
