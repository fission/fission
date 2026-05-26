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

	"github.com/fission/fission/test/integration/framework"
)

// TestKubernetesWatchTrigger exercises the kubewatcher subsystem
// (pkg/kubewatcher): a KubernetesWatchTrigger watching Services should invoke
// its function when a matching object is created. (The kubewatcher only
// supports POD/SERVICE/REPLICATIONCONTROLLER/JOB watch types.) We use the
// log.js fixture and assert the function's pod logs eventually contain
// "log test" after a watched Service is created.
func TestKubernetesWatchTrigger(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-kw-" + ns.ID
	fnName := "fn-kw-" + ns.ID
	kwName := "kw-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: framework.WriteTestData(t, "nodejs/log/log.js"),
	})

	ns.CreateKubernetesWatchTrigger(t, ctx, framework.KubernetesWatchTriggerOptions{
		Name: kwName, Function: fnName, ObjType: "service", WatchNamespace: ns.Name,
	})

	// Give the watcher a moment to establish its informer, then create a
	// Service in the watched namespace to fire an add event.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NotEmpty(c, ns.GetKubernetesWatchTriggerConditions(t, ctx, kwName),
			"kuberneteswatchtrigger should have status conditions")
	}, 1*time.Minute, 2*time.Second)

	ns.CreateService(t, ctx, "kw-watched-"+ns.ID)

	// The add event should drive an invocation, specializing a pod that logs.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Contains(c, ns.FunctionLogs(t, ctx, fnName), "log test",
			"watch-triggered invocation should produce function logs")
	}, 4*time.Minute, 5*time.Second)
}
