// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestEnvUpdatePreservesLabels guards the metadata contract of `fission env
// update`, which does a re-fetch-and-reapply through util.UpdateOnConflict: a
// `--labels` update must persist, and an update that does NOT pass --labels must
// leave existing labels untouched (the mutate only overwrites metadata the
// command was given, not a stale whole-object snapshot). `fn update` and
// `fn update-container` share the same code path. The conflict-retry mechanics
// themselves are covered by util.TestUpdateOnConflict* unit tests.
func TestEnvUpdatePreservesLabels(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	envName := "env-labels-" + ns.ID
	// The image is only stored on the CR (no pull happens until a function uses
	// the env), so a placeholder keeps this test image-independent and fast.
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: "fission/python-env"})
	envs := f.FissionClient().CoreV1().Environments(ns.Name)

	// A --labels update must persist on the CR.
	ns.CLI(t, ctx, "env", "update", "--name", envName, "--labels", "rfc=retry,team=fission")
	env, err := envs.Get(ctx, envName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "retry", env.Labels["rfc"], "env update --labels must persist on the CR")
	assert.Equal(t, "fission", env.Labels["team"], "env update --labels must persist on the CR")

	// A later update that does not mention labels must leave them intact —
	// proving the mutate doesn't clobber metadata it wasn't asked to change.
	ns.CLI(t, ctx, "env", "update", "--name", envName, "--graceperiod", "30")
	env, err = envs.Get(ctx, envName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "retry", env.Labels["rfc"], "a non-label update must preserve existing labels")
	assert.Equal(t, "fission", env.Labels["team"], "a non-label update must preserve existing labels")
}
