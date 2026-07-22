// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func stateTestFn(state *fv1.StateConfig) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "counter", Namespace: "user-ns"},
		Spec: fv1.FunctionSpec{
			Package: fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Name: "pkg", Namespace: "user-ns"}},
			State:   state,
		},
	}
}

func stateTestEnv() *fv1.Environment {
	return &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "node", Namespace: "user-ns"},
		Spec:       fv1.EnvironmentSpec{Version: 2},
	}
}

func TestNewSpecializeRequestStateKeyspace(t *testing.T) {
	t.Parallel()
	cfg, err := MakeFetcherConfig("/userfunc")
	require.NoError(t, err)

	t.Run("no state config: empty keyspace", func(t *testing.T) {
		t.Parallel()
		req := cfg.NewSpecializeRequest(stateTestFn(nil), stateTestEnv())
		assert.Empty(t, req.LoadReq.StateKeyspace)
	})

	t.Run("state config: keyspace defaults to fn name", func(t *testing.T) {
		t.Parallel()
		req := cfg.NewSpecializeRequest(stateTestFn(&fv1.StateConfig{}), stateTestEnv())
		assert.Equal(t, "counter", req.LoadReq.StateKeyspace)
	})

	t.Run("explicit keyspace wins", func(t *testing.T) {
		t.Parallel()
		req := cfg.NewSpecializeRequest(stateTestFn(&fv1.StateConfig{Keyspace: "carts"}), stateTestEnv())
		assert.Equal(t, "carts", req.LoadReq.StateKeyspace)
	})

	t.Run("infinite env: no token minted (specialize-time S1 backstop)", func(t *testing.T) {
		t.Parallel()
		env := stateTestEnv()
		env.Spec.AllowedFunctionsPerContainer = fv1.AllowedFunctionsPerContainerInfinite
		req := cfg.NewSpecializeRequest(stateTestFn(&fv1.StateConfig{Keyspace: "carts"}), env)
		assert.Empty(t, req.LoadReq.StateKeyspace, "an infinite env must not receive a scoped token")
	})
}

// TestSpecializePayloadCarriesNoToken pins the security property the
// pre-implementation review demanded: the -specialize-request CLI arg is
// visible to anyone who can read pods, so it must carry only the NON-SECRET
// keyspace name — never a token or the master secret.
func TestSpecializePayloadCarriesNoToken(t *testing.T) {
	t.Parallel()
	cfg, err := MakeFetcherConfig("/userfunc")
	require.NoError(t, err)

	podSpec := apiv1.PodSpec{Containers: []apiv1.Container{{Name: "node"}}}
	require.NoError(t, cfg.AddSpecializingFetcherToPodSpec(&podSpec, "node", "user-ns",
		stateTestFn(&fv1.StateConfig{Keyspace: "carts"}), stateTestEnv()))

	var fetcherContainer *apiv1.Container
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == "fetcher" {
			fetcherContainer = &podSpec.Containers[i]
		}
	}
	require.NotNil(t, fetcherContainer, "fetcher sidecar added")

	payload := ""
	for i, arg := range fetcherContainer.Command {
		if arg == "-specialize-request" && i+1 < len(fetcherContainer.Command) {
			payload = fetcherContainer.Command[i+1]
		}
	}
	require.NotEmpty(t, payload, "specialize request rides the command line")
	assert.Contains(t, payload, `"stateKeyspace":"carts"`)
	assert.NotContains(t, strings.ToLower(payload), "token")
	assert.NotContains(t, strings.ToLower(payload), "secret\":")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &decoded))
}
