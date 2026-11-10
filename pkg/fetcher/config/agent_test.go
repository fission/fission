// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func agentTestFn(agent *fv1.AgentConfig) *fv1.Function {
	return &fv1.Function{
		Name: "support-desk", Namespace: "user-ns",
		Spec: fv1.FunctionSpec{
			Package: fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Name: "pkg", Namespace: "user-ns"}},
			Agent:   agent,
		},
	}
}

func agentTestEnv() *fv1.Environment {
	return &fv1.Environment{
		Name: "node", Namespace: "user-ns",
		Spec: fv1.EnvironmentSpec{Version: 2},
	}
}

func TestNewSpecializeRequestAgentName(t *testing.T) {
	t.Parallel()
	cfg, err := MakeFetcherConfig("/userfunc")
	require.NoError(t, err)

	t.Run("not an agent: empty name", func(t *testing.T) {
		t.Parallel()
		req := cfg.NewSpecializeRequest(agentTestFn(nil), agentTestEnv())
		assert.Empty(t, req.LoadReq.AgentName)
	})

	t.Run("agent function on a finite env: name is the function name", func(t *testing.T) {
		t.Parallel()
		req := cfg.NewSpecializeRequest(agentTestFn(&fv1.AgentConfig{}), agentTestEnv())
		assert.Equal(t, "support-desk", req.LoadReq.AgentName)
	})

	t.Run("infinite env: no token minted (specialize-time S1 backstop)", func(t *testing.T) {
		t.Parallel()
		env := agentTestEnv()
		env.Spec.AllowedFunctionsPerContainer = fv1.AllowedFunctionsPerContainerInfinite
		req := cfg.NewSpecializeRequest(agentTestFn(&fv1.AgentConfig{}), env)
		assert.Empty(t, req.LoadReq.AgentName, "an infinite env must not receive a scoped agent identity token")
	})
}
