// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestBackendContainer exercises the container executor backend — the third
// backend alongside poolmgr and newdeploy, which previously had no e2e
// coverage. A container-executor function runs the user image directly (no
// environment pod); the executor builds a Deployment + Service and the router
// proxies to it.
//
// CONTAINER_RUNTIME_IMAGE must point at an image that serves HTTP and returns
// 2xx on GET /. The port it listens on is taken from CONTAINER_RUNTIME_PORT
// (default 8888); CI sets both so the image and the route agree.
func TestBackendContainer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	image := f.Images().RequireContainer(t)

	port := 8888
	if v := os.Getenv("CONTAINER_RUNTIME_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		require.NoErrorf(t, err, "CONTAINER_RUNTIME_PORT=%q must be an integer", v)
		port = p
	}

	ns := f.NewTestNamespace(t)
	fnName := "fn-ctr-" + ns.ID

	ns.CreateContainerFunction(t, ctx, framework.ContainerFunctionOptions{
		Name:  fnName,
		Image: image,
		Port:  port,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	// The container image owns its response body, so assert on the HTTP
	// status only: a 2xx proves the executor created the Deployment/Service
	// and the router proxied to the user container.
	f.Router(t).GetEventually(t, ctx, "/"+fnName, func(status int, _ string) bool {
		return status >= http.StatusOK && status < http.StatusMultipleChoices
	})
}
