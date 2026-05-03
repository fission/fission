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

// TestIngress is the Go port of test_ingress.sh (was bash-disabled-existing
// because Kind has no ingress controller). We verify the *Ingress object*
// the fission router controller creates/updates from the HTTPTrigger spec
// — that's what the bash version's `kubectl get ing -l ...` checked. The
// live HTTP-via-ingress request at the end of the bash test isn't ported
// here because Kind doesn't ship an ingress controller; that should run
// in GKE/EKS-flavored CI when we get there.
func TestIngress(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-ing-" + ns.ID
	fnName := "fn-ing-" + ns.ID
	routeName := "ing-" + ns.ID
	relativeURL := "/itest-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
	})

	// Initial trigger with --createingress and no host/annotations/tls.
	// Use raw CLI since RouteOptions doesn't expose the ingress flags
	// (and they're trigger-update specific).
	ns.CLI(t, ctx, "route", "create",
		"--name", routeName, "--url", relativeURL, "--method", "GET",
		"--function", fnName, "--createingress")
	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = f.FissionClient().CoreV1().HTTPTriggers(ns.Name).Delete(dctx, routeName, metav1.DeleteOptions{})
	})

	// The fission router controller reconciles HTTPTriggers to Ingress
	// objects asynchronously. The controller labels the Ingress with
	// functionName= and triggerName= so we can find it without parsing
	// CLI output.
	listSel := "functionName=" + fnName + ",triggerName=" + routeName

	requireIngress := func(t *testing.T, expectPath, expectHost, expectTLS string) {
		t.Helper()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			// pkg/router/ingress.go creates Ingresses in POD_NAMESPACE
			// (the router pod's own ns, default `fission`), not in the
			// trigger's namespace. Search across all namespaces.
			list, err := f.KubeClient().NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
				LabelSelector: listSel,
			})
			if !assert.NoErrorf(c, err, "list ingresses") {
				return
			}
			if !assert.NotEmptyf(c, list.Items, "no Ingress for trigger %q yet", routeName) {
				return
			}
			ing := list.Items[0]
			if !assert.NotEmptyf(c, ing.Spec.Rules, "ingress %q has no rules", ing.Name) {
				return
			}
			rule := ing.Spec.Rules[0]
			if !assert.NotNilf(c, rule.HTTP, "ingress %q rule has no HTTP", ing.Name) ||
				!assert.NotEmptyf(c, rule.HTTP.Paths, "ingress %q rule has no paths", ing.Name) {
				return
			}
			assert.Equalf(c, expectPath, rule.HTTP.Paths[0].Path,
				"ingress %q path mismatch", ing.Name)
			assert.Equalf(c, expectHost, rule.Host,
				"ingress %q host mismatch", ing.Name)
			if expectTLS == "" {
				assert.Emptyf(c, ing.Spec.TLS, "ingress %q expected no TLS", ing.Name)
			} else {
				if assert.NotEmptyf(c, ing.Spec.TLS, "ingress %q expected TLS secret", ing.Name) {
					assert.Equalf(c, expectTLS, ing.Spec.TLS[0].SecretName,
						"ingress %q TLS secret mismatch", ing.Name)
				}
			}
		}, 60*time.Second, 2*time.Second)
	}

	// Phase 1 — defaults. Path = relativeURL, no host, no TLS.
	requireIngress(t, relativeURL, "", "")

	// Phase 2 — update with host, custom path, annotation, TLS.
	hostName := "test-" + ns.ID + ".com"
	ns.CLI(t, ctx, "route", "update", "--name", routeName,
		"--function", fnName,
		"--ingressannotation", "foo=bar",
		"--ingressrule", hostName+"=/foo/bar",
		"--ingresstls", "dummy")
	requireIngress(t, "/foo/bar", hostName, "dummy")

	// Phase 3 — clear all the optional fields with `-`.
	ns.CLI(t, ctx, "route", "update", "--name", routeName,
		"--function", fnName,
		"--ingressannotation", "-",
		"--ingressrule", "-",
		"--ingresstls", "-")
	requireIngress(t, relativeURL, "", "")
}
