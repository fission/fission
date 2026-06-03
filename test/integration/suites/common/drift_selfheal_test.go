// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestDriftSelfHeal verifies the executor's drift watch: a newdeploy function's
// backing Deployment deleted out-of-band is recreated proactively — without an
// invocation — by the shared Function reconciler's .Watches() on Deployments.
//
// The test deliberately does NOT call the function after deleting the Deployment:
// the request path (GetFuncSvc → IsValid → re-specialize) would also recreate it,
// so to prove the *watch* healed it we wait on the Deployment reappearing with a
// fresh UID without sending any traffic.
func TestDriftSelfHeal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-drift-" + ns.ID
	fnName := "fn-drift-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 2,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))

	// Capture the live Deployment, then delete it out-of-band.
	orig := ns.FunctionDeployment(t, ctx, fnName)
	origUID := orig.UID
	err := f.KubeClient().AppsV1().Deployments(orig.Namespace).Delete(ctx, orig.Name, metav1.DeleteOptions{})
	require.NoErrorf(t, err, "deleting deployment %s/%s out-of-band", orig.Namespace, orig.Name)

	// The drift watch must recreate it — a Deployment with a fresh UID — without
	// any request driving the recreation.
	ns.WaitForFunctionDeployment(t, ctx, fnName, func(d *appsv1.Deployment) bool {
		return d.UID != origUID
	}, "drift watch must recreate the deleted Deployment without an invocation", 90*time.Second)
}
