//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestSpecMerge is the Go port of test_specs/test_spec_merge/test_spec_merge.sh.
// The bash version was deferred earlier because the vendored YAMLs ship with
// hardcoded resource names that would collide under t.Parallel; this port
// uses MaterializeSpecs to substitute TEST_ID-suffixed names + a fresh
// DeploymentConfig UID before `fission spec apply`.
//
// Coverage:
//   - poolmgr Function (nodehellop/nodep) and newdeploy Function
//     (nodehellond/nodend) come up via spec apply
//   - both serve traffic over the router's internal /fission-function/<fn>
//   - the env spec's `runtime.podspec.hostname=foo-bar` propagates through
//     to the spawned Deployment's pod spec, on both executor types.
func TestSpecMerge(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	// Spec yamls hardcode the runtime image (ghcr.io/fission/node-env);
	// require the env image so this is skipped in CI envs without the node
	// runtime preloaded.
	_ = f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)

	envP := "nodep-" + ns.ID
	envND := "nodend-" + ns.ID
	fnP := "nodehellop-" + ns.ID
	fnND := "nodehellond-" + ns.ID
	pkgName := "hellojs-" + ns.ID
	archive := "helloarch-" + ns.ID
	uid := framework.NewSpecUID(t)

	repls := map[string]string{
		// Functions (longest match first, handled internally by replacer).
		"nodehellond": fnND,
		"nodehellop":  fnP,
		"nodend":      envND,
		"nodep":       envP,
		// The package + archive names ship as random spec-init suffixes
		// (`hello-js-vm2y`, `hello-js-leSC`); regenerate them too so
		// concurrent tests don't collide on resource names in `default`.
		"hello-js-vm2y": pkgName,
		"hello-js-leSC": archive,
		// DeploymentConfig name + uid scope the `spec destroy` cleanup
		// label-selector to just this test's resources.
		"name: spec-merge": "name: " + "spec-merge-" + ns.ID,
		"b1573a35-f3a2-45a8-a430-1f1a08d71177": uid,
	}

	workdir := t.TempDir()
	framework.MaterializeSpecs(t, "nodejs/spec_merge", repls, workdir)

	// `spec apply` reads ./specs and uploads `hello.js` as the package
	// archive; cwd needs both, hence WithCWD.
	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "apply")
	})
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dcancel()
		ns.WithCWD(t, workdir, func() {
			_ = ns.CLI(t, dctx, "spec", "destroy")
		})
	})

	body := f.Router(t).GetEventually(t, ctx, "/fission-function/"+fnP,
		framework.BodyContains("hello"))
	require.True(t, strings.Contains(strings.ToLower(body), "hello"),
		"poolmgr fn %q response missing 'hello': %q", fnP, body)

	body = f.Router(t).GetEventually(t, ctx, "/fission-function/"+fnND,
		framework.BodyContains("hello"))
	require.True(t, strings.Contains(strings.ToLower(body), "hello"),
		"newdeploy fn %q response missing 'hello': %q", fnND, body)

	// The env podspec sets hostname=foo-bar; the router controller
	// merges that into both the poolmgr-pool Deployment and the
	// newdeploy Function Deployment. Verify on both.
	checkHostname := func(label string) {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			deps, err := f.KubeClient().AppsV1().Deployments(ns.Name).List(ctx, metav1.ListOptions{
				LabelSelector: label,
			})
			if !assert.NoErrorf(c, err, "list deployments %q", label) {
				return
			}
			if !assert.NotEmptyf(c, deps.Items, "no deployment for %q", label) {
				return
			}
			assert.Equalf(c, "foo-bar", deps.Items[0].Spec.Template.Spec.Hostname,
				"deployment %q should have podspec hostname=foo-bar",
				deps.Items[0].Name)
		}, 60*time.Second, 2*time.Second)
	}
	checkHostname("functionName=" + fnND)    // newdeploy
	checkHostname("environmentName=" + envP) // poolmgr (env-level deployment)
}
