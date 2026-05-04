//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestEnvironmentAnnotations is the Go port of test/tests/test_annotations.sh.
// It verifies that `metadata.annotations` set on an Environment CR propagate
// to the runtime and builder pods Fission spawns. The bash test had to use
// `kubectl apply` because the CLI doesn't expose env-level annotations; the
// Go test uses the typed Fission clientset directly via CreateEnvObject.
//
// Note: bash had a copy-paste bug that effectively only checked the runtime
// pod twice. We check both runtime and builder pods correctly.
func TestEnvironmentAnnotations(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "python-anno-" + ns.ID
	const annKey, annValue = "fission.io/integration-test-annotation", "set-from-env-meta"

	ns.CreateEnvObject(t, ctx, &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        envName,
			Annotations: map[string]string{annKey: annValue},
		},
		Spec: fv1.EnvironmentSpec{
			Version:  2,
			Runtime:  fv1.Runtime{Image: runtime},
			Builder:  fv1.Builder{Image: builder, Command: "build"},
			Poolsize: 1,
		},
	})

	// Both runtime (poolmgr) and builder pods carry the `envName=<env>`
	// label. We poll until at least one pod is observed for each component
	// and assert the annotation is propagated.
	requirePodAnnotation(t, ctx, ns, "envName="+envName, annKey, annValue)
}

// requirePodAnnotation polls until at least one pod matching the label
// selector exists in the namespace, then asserts every matching pod carries
// the annotation. Polls for up to 3 minutes — runtime pool pods can be slow
// to schedule on a freshly-loaded Kind cluster image.
func requirePodAnnotation(t *testing.T, ctx context.Context, ns *framework.TestNamespace, labelSelector, key, want string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := ns.Framework().KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if !assert.NoErrorf(c, err, "list pods (%s)", labelSelector) {
			return
		}
		if !assert.NotEmptyf(c, pods.Items, "no pods yet for selector %q", labelSelector) {
			return
		}
		for _, p := range pods.Items {
			assert.Equalf(c, want, p.Annotations[key],
				"pod %q annotation %q", p.Name, key)
		}
	}, 3*time.Minute, 2*time.Second)
}
