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

// TestTimeTrigger exercises the timer subsystem (pkg/timer): a TimeTrigger on a
// short cron schedule should repeatedly invoke its function. We use the log.js
// fixture (which logs "log test" on each call) and assert the function's pod
// logs eventually contain that line — proof the timer fired and the router
// dispatched the invocation.
func TestTimeTrigger(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-tt-" + ns.ID
	fnName := "fn-tt-" + ns.ID
	ttName := "tt-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: framework.WriteTestData(t, "nodejs/log/log.js"),
	})

	ns.CreateTimeTrigger(t, ctx, framework.TimeTriggerOptions{
		Name: ttName, Function: fnName, Cron: "@every 15s",
	})

	// The controller should record the trigger's schedule in its status.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NotEmpty(c, ns.GetTimeTriggerConditions(t, ctx, ttName), "timetrigger should have status conditions")
	}, 1*time.Minute, 2*time.Second)

	// Within a few cron ticks the timer should have invoked the function,
	// specializing a pod that logs "log test".
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Contains(c, ns.FunctionLogs(t, ctx, fnName), "log test",
			"timer-triggered invocation should produce function logs")
	}, 4*time.Minute, 5*time.Second)
}
