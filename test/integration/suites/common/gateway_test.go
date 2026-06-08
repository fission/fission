// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"

	"github.com/fission/fission/test/integration/framework"
)

// TestGatewayRoute verifies the router's Gateway API route provider: the
// HTTPRoute object the router creates/updates/deletes from an HTTPTrigger whose
// routeConfig.provider is "gateway".
//
// It is gated on TEST_GATEWAY_PARENTREF (e.g. "envoy-gateway/eg" or "eg"): the
// test only runs against a cluster that has the Gateway API CRDs installed, a
// Gateway controller, a Gateway to attach to, and the router started with
// GATEWAY_API_ENABLED=true. Kind CI ships none of these, so the test skips
// there — mirroring how the runtime-image-gated tests skip when their image env
// is unset. The live HTTP-via-gateway request is not asserted here (it depends
// on the operator's Gateway listener address); this checks the object Fission
// owns, the same way TestIngress checks the Ingress object.
func TestGatewayRoute(t *testing.T) {
	t.Parallel()

	parentRef := os.Getenv("TEST_GATEWAY_PARENTREF")
	if parentRef == "" {
		t.Skip("TEST_GATEWAY_PARENTREF unset; skipping Gateway API route test (needs Gateway API CRDs, a Gateway, and the router started with GATEWAY_API_ENABLED)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	gwClient, err := gatewayclient.NewForConfig(f.RestConfig())
	require.NoError(t, err, "build gateway api client")

	ns := f.NewTestNamespace(t)
	envName := "node-gw-" + ns.ID
	fnName := "fn-gw-" + ns.ID
	routeName := "gw-" + ns.ID
	relativeURL := "/gwtest-" + ns.ID
	hostName := "gw-" + ns.ID + ".example.com"

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
	})

	ns.CLI(t, ctx, "route", "create",
		"--name", routeName, "--url", relativeURL, "--method", "GET",
		"--function", fnName,
		"--route-provider", "gateway",
		"--route-host", hostName,
		"--gateway", parentRef)
	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = f.FissionClient().CoreV1().HTTPTriggers(ns.Name).Delete(dctx, routeName, metav1.DeleteOptions{})
	})

	// The router labels the HTTPRoute with functionName= and triggerName= (via
	// util.GetDeployLabels), and creates it in POD_NAMESPACE (the router pod's
	// own namespace), not the trigger's namespace.
	listSel := "functionName=" + fnName + ",triggerName=" + routeName

	// Parse the expected parentRef name (drop any "namespace/" prefix).
	wantParentName := parentRef
	if _, name, ok := strings.Cut(parentRef, "/"); ok {
		wantParentName = name
	}

	requireHTTPRoute := func(t *testing.T, expectPath, expectHost string, expectExists bool) {
		t.Helper()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			list, err := gwClient.GatewayV1().HTTPRoutes(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
				LabelSelector: listSel,
			})
			if !assert.NoErrorf(c, err, "list httproutes") {
				return
			}
			if !expectExists {
				assert.Emptyf(c, list.Items, "HTTPRoute for trigger %q should be gone", routeName)
				return
			}
			if !assert.NotEmptyf(c, list.Items, "no HTTPRoute for trigger %q yet", routeName) {
				return
			}
			hr := list.Items[0]
			if assert.NotEmptyf(c, hr.Spec.ParentRefs, "httproute %q has no parentRefs", hr.Name) {
				assert.Equalf(c, gwapiv1.ObjectName(wantParentName), hr.Spec.ParentRefs[0].Name,
					"httproute %q parentRef name mismatch", hr.Name)
			}
			if assert.NotEmptyf(c, hr.Spec.Hostnames, "httproute %q has no hostnames", hr.Name) {
				assert.Equalf(c, gwapiv1.Hostname(expectHost), hr.Spec.Hostnames[0],
					"httproute %q hostname mismatch", hr.Name)
			}
			if assert.NotEmptyf(c, hr.Spec.Rules, "httproute %q has no rules", hr.Name) &&
				assert.NotEmptyf(c, hr.Spec.Rules[0].Matches, "httproute %q rule has no matches", hr.Name) {
				match := hr.Spec.Rules[0].Matches[0]
				if assert.NotNilf(c, match.Path, "httproute %q match has no path", hr.Name) &&
					assert.NotNilf(c, match.Path.Value, "httproute %q path value is nil", hr.Name) {
					assert.Equalf(c, expectPath, *match.Path.Value, "httproute %q path mismatch", hr.Name)
				}
			}
		}, 60*time.Second, 2*time.Second)
	}

	// Created with the trigger URL as the path and the configured host.
	requireHTTPRoute(t, relativeURL, hostName, true)

	// End-to-end signal beyond Fission writing the object: the Gateway
	// implementation (Envoy Gateway in CI) must Accept the route, proving the
	// HTTPRoute Fission generated is valid and attaches to the parent Gateway.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		list, err := gwClient.GatewayV1().HTTPRoutes(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: listSel})
		if !assert.NoErrorf(c, err, "list httproutes") || !assert.NotEmpty(c, list.Items) {
			return
		}
		accepted := false
		for _, p := range list.Items[0].Status.Parents {
			for _, cond := range p.Conditions {
				if cond.Type == string(gwapiv1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
					accepted = true
				}
			}
		}
		assert.Truef(c, accepted, "HTTPRoute %q not yet Accepted by the gateway", routeName)
	}, 120*time.Second, 3*time.Second)

	// Update path + host.
	newHost := "gw2-" + ns.ID + ".example.com"
	ns.CLI(t, ctx, "route", "update", "--name", routeName,
		"--function", fnName, "--url", relativeURL,
		"--route-path", "/v2",
		"--route-host", newHost,
		"--gateway", parentRef)
	requireHTTPRoute(t, "/v2", newHost, true)

	// Switch provider off (back to no external route) -> HTTPRoute removed.
	ns.CLI(t, ctx, "route", "update", "--name", routeName,
		"--function", fnName, "--url", relativeURL,
		"--route-provider", "ingress")
	requireHTTPRoute(t, "", "", false)
}
