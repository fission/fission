// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func getEnvInput(output string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.SetString(flagkey.EnvName, "nodejs")
	if output != "" {
		in.SetString(flagkey.Output, output)
	}
	return in
}

func setGetEnvClients(t *testing.T, env *fv1.Environment) {
	t.Helper()
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(env),
		Namespace:        "default",
	})
}

// TestEnvironmentGet pins the behaviors `env get` gained when it was routed
// through util.PrintGet — it was the one get subcommand with no -o json/yaml
// and no CONDITIONS block — and that the table kept its historical two
// columns so scripted output stays parseable.
func TestEnvironmentGet(t *testing.T) {
	env := &fv1.Environment{
		Name: "nodejs", Namespace: "default",
		Spec: fv1.EnvironmentSpec{Version: 2, Runtime: fv1.Runtime{Image: "fission/node-env"}},
		Status: fv1.EnvironmentStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "PoolReady", Message: "pool is up"},
		}},
	}

	t.Run("table output keeps the historical columns and gains conditions", func(t *testing.T) {
		setGetEnvClients(t, env)
		out := captureStdout(t, func() error { return Get(getEnvInput("")) })

		assert.Contains(t, out, "NAME   IMAGE\n", "historical two-column header")
		assert.Contains(t, out, "nodejs fission/node-env")
		assert.Contains(t, out, "CONDITIONS:")
		assert.Contains(t, out, "PoolReady")
	})

	t.Run("-o json renders the whole object", func(t *testing.T) {
		setGetEnvClients(t, env)
		out := captureStdout(t, func() error { return Get(getEnvInput("json")) })

		var got fv1.Environment
		require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be valid JSON:\n%s", out)
		assert.Equal(t, "nodejs", got.Name)
		assert.Equal(t, "fission/node-env", got.Spec.Runtime.Image)
	})

	t.Run("an unknown -o value is an error", func(t *testing.T) {
		setGetEnvClients(t, env)
		err := Get(getEnvInput("banana"))
		require.Error(t, err)
	})
}
