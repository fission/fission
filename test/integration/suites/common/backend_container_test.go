// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/fission/fission/test/integration/framework"
)

// TestBackendContainer exercises the container executor backend — the third
// backend alongside poolmgr and newdeploy, which previously had no e2e
// coverage. A container-executor function runs the user image directly (no
// environment pod); the executor builds a Deployment + Service and the router
// proxies to it.
//
// CONTAINER_RUNTIME_IMAGE must point at an image that serves HTTP and returns
// 2xx on GET /, and that honours a PORT env var (e.g. gcr.io/google-samples/
// hello-app). The function is run on 8888 — the port the function-pods
// NetworkPolicy permits the router to reach (poolmgr/newdeploy env runtimes
// use 8888 too). We inject PORT=8888 via a ConfigMap (the container executor
// surfaces configmap keys as env vars) so the user image listens there.
func TestBackendContainer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	image := f.Images().RequireContainer(t)

	const port = 8888

	ns := f.NewTestNamespace(t)
	fnName := "fn-ctr-" + ns.ID
	cmName := "ctr-port-" + ns.ID

	// Tell the image (via PORT env, injected from this configmap) to listen on
	// the NetworkPolicy-permitted port.
	ns.CreateConfigMap(t, ctx, cmName, map[string]string{"PORT": strconv.Itoa(port)})

	ns.CreateContainerFunction(t, ctx, framework.ContainerFunctionOptions{
		Name:       fnName,
		Image:      image,
		Port:       port,
		ConfigMaps: []string{cmName},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	// The container image owns its response body, so assert on the HTTP
	// status only: a 2xx proves the executor created the Deployment/Service
	// and the router proxied to the user container.
	f.Router(t).GetEventually(t, ctx, "/"+fnName, func(status int, _ string) bool {
		return status >= http.StatusOK && status < http.StatusMultipleChoices
	})
}
