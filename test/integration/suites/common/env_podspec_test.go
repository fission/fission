//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestEnvPodSpec is the Go port of test/tests/test_env_podspec.sh. It
// exercises the env CR's runtime/builder PodSpec field — specifically the
// initContainers added to both the runtime (poolmgr) pod and the builder
// pod. Both pods should reach Ready, and their initContainers should
// terminate with reason=Completed.
//
// The bash test also covers a "negative" scenario where mismatched container
// names (podspec.containers[].name vs container.name) cause the deployment
// not to be created. Skipped here — it's testing fission's webhook
// validation, which deserves its own unit-test scope rather than
// integration.
func TestEnvPodSpec(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "python-podspec-" + ns.ID

	envObj := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: envName},
		Spec: fv1.EnvironmentSpec{
			Version:  3,
			Poolsize: 1,
			Runtime: fv1.Runtime{
				Image: runtime,
				Container: &corev1.Container{
					Name: envName,
				},
				PodSpec: &corev1.PodSpec{
					Containers: []corev1.Container{{Name: envName}},
					InitContainers: []corev1.Container{{
						Name:    "init",
						Image:   "alpine",
						Command: []string{"sleep", "1"},
					}},
				},
			},
			Builder: fv1.Builder{
				Image:   builder,
				Command: "build",
				Container: &corev1.Container{
					Name: "builder",
				},
				PodSpec: &corev1.PodSpec{
					Containers: []corev1.Container{{Name: "builder"}},
					InitContainers: []corev1.Container{{
						Name:    "init",
						Image:   "alpine",
						Command: []string{"sleep", "1"},
					}},
				},
			},
		},
	}
	ns.CreateEnvObject(t, ctx, envObj)
	ns.WaitForBuilderReady(t, ctx, envName)

	// Verify each pod (matching environmentName for runtime, envName for
	// builder — see batch 2 fix #2 commit) carries an init container that
	// reached Completed.
	for _, sel := range []string{
		"environmentName=" + envName,
		"envName=" + envName,
	} {
		assertInitCompleted(t, ctx, ns, sel)
	}
}

func assertInitCompleted(t *testing.T, ctx context.Context, ns *framework.TestNamespace, labelSelector string) {
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
			if !assert.NotEmptyf(c, p.Status.InitContainerStatuses,
				"pod %q has no initContainerStatuses yet", p.Name) {
				continue
			}
			s := p.Status.InitContainerStatuses[0]
			if !assert.NotNilf(c, s.State.Terminated, "init container of %q not yet terminated", p.Name) {
				continue
			}
			assert.Equalf(c, "Completed", s.State.Terminated.Reason,
				"init container of %q terminated reason = %q (want Completed)",
				p.Name, s.State.Terminated.Reason)
		}
	}, 3*time.Minute, 2*time.Second)
}
