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

// TestEnvVars is the Go port of test/tests/test_env_vars.sh (was bash-disabled
// per Phase-5 triage). Verifies that env vars set on
// Environment.Spec.Runtime.Container.Env and Builder.Container.Env propagate
// to the spawned pool / builder pods.
//
// The bash version exec'd into the pods and ran `env`. We instead inspect
// each pod's Spec.Containers[].Env via the typed clientset, which is what
// the bash command would have read anyway — same propagation guarantee
// without the SPDY/exec plumbing.
func TestEnvVars(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "py-envvars-" + ns.ID
	const runtimeKey, runtimeVal = "TEST_RUNTIME_ENV_KEY", "TEST_RUNTIME_ENV_VAR"
	const builderKey, builderVal = "TEST_BUILDER_ENV_KEY", "TEST_BUILDER_ENV_VAR"

	ns.CreateEnvObject(t, ctx, &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: envName},
		Spec: fv1.EnvironmentSpec{
			Version:  2,
			Poolsize: 1,
			Runtime: fv1.Runtime{
				Image: runtime,
				Container: &corev1.Container{
					Env: []corev1.EnvVar{{Name: runtimeKey, Value: runtimeVal}},
				},
			},
			Builder: fv1.Builder{
				Image:   builder,
				Command: "build",
				Container: &corev1.Container{
					Env: []corev1.EnvVar{{Name: builderKey, Value: builderVal}},
				},
			},
		},
	})

	// Wait for both pods to materialize. CreateEnvObject doesn't auto-wait
	// (unlike CreateEnv). Builder pod carries `envName=<env>`, runtime pod
	// carries `environmentName=<env>`.
	ns.WaitForBuilderReady(t, ctx, envName)
	ns.WaitForRuntimePodReady(t, ctx, envName)

	// Verify the runtime pod's container has the runtime env var.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "environmentName=" + envName,
		})
		if !assert.NoError(c, err) {
			return
		}
		if !assert.NotEmptyf(c, pods.Items, "no runtime pods for env %q", envName) {
			return
		}
		assert.Truef(c, podHasEnv(&pods.Items[0], envName, runtimeKey, runtimeVal),
			"runtime container missing env var %s=%s in pod %s",
			runtimeKey, runtimeVal, pods.Items[0].Name)
	}, 60*time.Second, 2*time.Second)

	// Verify the builder pod's `builder` container has the builder env var.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "envName=" + envName,
		})
		if !assert.NoError(c, err) {
			return
		}
		if !assert.NotEmptyf(c, pods.Items, "no builder pods for env %q", envName) {
			return
		}
		assert.Truef(c, podHasEnv(&pods.Items[0], "builder", builderKey, builderVal),
			"builder container missing env var %s=%s in pod %s",
			builderKey, builderVal, pods.Items[0].Name)
	}, 60*time.Second, 2*time.Second)
}

// podHasEnv reports whether the named container in the pod has the given
// env var with the expected value.
func podHasEnv(p *corev1.Pod, containerName, key, want string) bool {
	for _, c := range p.Spec.Containers {
		if c.Name != containerName {
			continue
		}
		for _, e := range c.Env {
			if e.Name == key && e.Value == want {
				return true
			}
		}
	}
	return false
}
