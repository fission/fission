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

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionDescribe exercises `fission function describe` (RFC-0017) end to
// end: it aggregates the function summary, conditions, package/build status, and
// backing pods into one view. The command streams its rendered output to
// os.Stdout, so the test captures it via CLICaptureStdout (like `function
// logs`), warms a pod with one invocation, then asserts the consolidated view
// names the function, its environment, and the pods section.
func TestFunctionDescribe(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-desc-" + ns.ID
	fnName := "descfn-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	// Warm a pod so the PODS section has an entry to render.
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))

	out := ns.CLICaptureStdout(t, ctx, "function", "describe", "--name", fnName)

	assert.Containsf(t, out, fnName, "describe should name the function; got:\n%s", out)
	assert.Containsf(t, out, envName, "describe should name the environment; got:\n%s", out)
	assert.Containsf(t, out, "PODS", "describe should render the pods section; got:\n%s", out)
	// The warmed, specialized pod is labeled with the function name, so it shows
	// up under PODS.
	assert.Containsf(t, out, "poolmgr", "describe should show the poolmgr executor/pod; got:\n%s", out)
}
