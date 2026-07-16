// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	typedcorev1 "github.com/fission/fission/pkg/generated/clientset/versioned/typed/core/v1"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/test/integration/framework"
)

// startedRunName extracts the run name from `workflow run` output — parsing
// the "started" line specifically, because warnings share stdout.
func startedRunName(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`workflow run '([^']+)' started`).FindStringSubmatch(out)
	require.NotNilf(t, m, "no started line in output:\n%s", out)
	return m[1]
}

// waitForTerminalRun polls until the named run reaches a terminal phase and
// returns it.
func waitForTerminalRun(t *testing.T, ctx context.Context, runs typedcorev1.WorkflowRunInterface, runName string) *fv1.WorkflowRun {
	t.Helper()
	var run *fv1.WorkflowRun
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		run, err = runs.Get(ctx, runName, metav1.GetOptions{})
		require.NoError(c, err)
		require.True(c, run.Status.Phase.Terminal(), "phase %s", run.Status.Phase)
	}, 3*time.Minute, 2*time.Second, "run must reach a terminal phase")
	return run
}

// createWorkflow writes the manifest, creates it via the CLI, and registers
// deletion cleanup (runs cascade via their own cleanup below).
func createWorkflow(t *testing.T, ctx context.Context, f *framework.Framework, ns *framework.TestNamespace, wfName, manifest string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0o600))
	ns.CLI(t, ctx, "workflow", "create", "-f", path)
	t.Cleanup(func() {
		bg := context.Background()
		runs, _ := f.FissionClient().CoreV1().WorkflowRuns(ns.Name).List(bg, metav1.ListOptions{})
		if runs != nil {
			for _, r := range runs.Items {
				if r.Spec.WorkflowRef == wfName {
					_ = f.FissionClient().CoreV1().WorkflowRuns(ns.Name).Delete(bg, r.Name, metav1.DeleteOptions{})
				}
			}
		}
		_ = f.FissionClient().CoreV1().Workflows(ns.Name).Delete(bg, wfName, metav1.DeleteOptions{})
	})
}

const orderPipelineManifest = `
apiVersion: fission.io/v1
kind: Workflow
metadata:
  name: order-pipeline
spec:
  startAt: validate
  timeout: 1h
  defaultRetry: { maxAttempts: 3, backoffBase: 2s, backoffCap: 30s }
  states:
    validate:
      type: Task
      function: { name: validate-order }
      timeout: 30s
      catch:
        - errorType: Fission.PermanentError
          next: reject
      next: charge
    charge:
      type: Task
      function: { name: charge-card }
      retry: { maxAttempts: 5, backoffBase: 1s, backoffCap: 60s }
      catch:
        - errorType: PaymentDeclined
          next: reject
      resultPath: $.charge
      next: fulfil
    fulfil:
      type: Task
      function: { name: fulfil-order }
      end: true
    reject:
      type: Task
      function: { name: notify-rejection }
      end: true
`

// TestWorkflowCRUD exercises the phase-1 RFC-0022 surface end to end
// against a live cluster: CLI create/list/validate/graph/delete on the
// RFC's worked-example manifest (verbatim — including the defaulted
// function-reference type), and webhook rejection of an invalid graph.
// No engine exists yet, so runs are not executed here.
func TestWorkflowCRUD(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	wfName := "order-pipeline-" + ns.ID
	manifest := strings.ReplaceAll(orderPipelineManifest, "order-pipeline", wfName)
	dir := t.TempDir()
	path := filepath.Join(dir, "order-pipeline.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0o600))
	t.Cleanup(func() {
		_ = f.FissionClient().CoreV1().Workflows(ns.Name).Delete(context.Background(), wfName, metav1.DeleteOptions{})
	})

	out := ns.CLICaptureStdout(t, ctx, "workflow", "create", "-f", path)
	assert.Contains(t, out, "created")

	// The mutating webhook defaulted the function-reference type.
	wf, err := f.FissionClient().CoreV1().Workflows(ns.Name).Get(ctx, wfName, metav1.GetOptions{})
	require.NoError(t, err)
	for name, st := range wf.Spec.States {
		require.NotNil(t, st.Function, "state %s", name)
		assert.EqualValues(t, fv1.FunctionReferenceTypeFunctionName, st.Function.Type, "state %s", name)
	}

	listOut := ns.CLICaptureStdout(t, ctx, "workflow", "list")
	assert.Contains(t, listOut, wfName)

	valOut := ns.CLICaptureStdout(t, ctx, "workflow", "validate", "-f", path, "--offline")
	assert.Contains(t, valOut, "valid")

	graphOut := ns.CLICaptureStdout(t, ctx, "workflow", "graph", "--name", wfName)
	assert.Contains(t, graphOut, "stateDiagram-v2")
	assert.Contains(t, graphOut, "validate --> charge")

	// The validating webhook rejects an unresolvable Next target.
	bad := strings.Replace(manifest, "next: charge", "next: ghost", 1)
	badPath := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(badPath, []byte(bad), 0o600))
	_, err = ns.CLICaptureStdoutBestEffort(t, ctx, "workflow", "create", "-f", badPath, "--name", "bad-pipeline-"+ns.ID)
	require.Error(t, err, "webhook must reject an unresolvable Next target")
	assert.Contains(t, err.Error(), "ghost")

	out = ns.CLICaptureStdout(t, ctx, "workflow", "delete", "--name", wfName)
	assert.Contains(t, out, "deleted")
}

// TestWorkflowEngineLinear runs the RFC's pipeline shape end to end against
// a live cluster: three chained Task states over a real node function, run
// via the CLI, driven by the workflow head, output asserted from status.
func TestWorkflowEngineLinear(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	envName := "nodejs-wf-" + ns.ID
	fnName := "hello-wf-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	// The fixture must return a JSON object: the node env's body-parser is
	// strict, so a plain-text step output (a bare JSON string) would 400 the
	// next state's dispatch. wf-step also increments a hop counter, proving
	// each state saw its predecessor's output.
	codePath := framework.WriteTestData(t, "nodejs/wf-step/wf-step.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	// Warm the function so the run's single default attempt (no retry policy)
	// can't be consumed by a router-cache 404 or cold-start hiccup.
	f.Router(t).GetEventually(t, ctx, utils.UrlForFunction(fnName, ns.Name), framework.BodyContains("hops"))

	wfName := "wf-linear-" + ns.ID
	manifest := fmt.Sprintf(`
apiVersion: fission.io/v1
kind: Workflow
metadata:
  name: %[2]s
spec:
  startAt: a
  states:
    a: {type: Task, function: {name: %[1]s}, next: b}
    b: {type: Task, function: {name: %[1]s}, next: c}
    c: {type: Task, function: {name: %[1]s}, end: true}
`, fnName, wfName)
	createWorkflow(t, ctx, f, ns, wfName, manifest)

	out := ns.CLICaptureStdout(t, ctx, "workflow", "run", "--name", wfName)
	runName := startedRunName(t, out)

	runs := f.FissionClient().CoreV1().WorkflowRuns(ns.Name)
	run := waitForTerminalRun(t, ctx, runs, runName)

	require.Equal(t, fv1.WorkflowRunSucceeded, run.Status.Phase)
	require.NotNil(t, run.Status.Output)
	assert.Contains(t, string(run.Status.Output.Raw), `"hops":3`,
		"each of the three states must have seen its predecessor's output")
	assert.True(t, meta.IsStatusConditionTrue(run.Status.Conditions, fv1.WorkflowRunConditionAccepted))

	// history + describe read back through the head's signed endpoint.
	hist := ns.CLICaptureStdout(t, ctx, "workflow", "history", "--name", runName)
	assert.Contains(t, hist, "RunStarted")
	assert.Contains(t, hist, "RunSucceeded")
	desc := ns.CLICaptureStdout(t, ctx, "workflow", "describe", "--name", runName)
	assert.Contains(t, desc, "Succeeded")
}

// TestWorkflowEngineRetryCatch drives the retry-then-catch path with a
// deliberately failing function: attempts hit the budget, the catch routes
// to a recovery function, and the run succeeds.
func TestWorkflowEngineRetryCatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	envName := "nodejs-wfrc-" + ns.ID
	failName := "fail-wf-" + ns.ID
	helloName := "hello-wfrc-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	failJS := filepath.Join(t.TempDir(), "fail.js")
	require.NoError(t, os.WriteFile(failJS, []byte(
		"module.exports = async function(context) { return { status: 500, body: JSON.stringify({failed: true}) }; }\n"), 0o600))
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: failName, Env: envName, Code: failJS})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: helloName, Env: envName, Code: codePath})
	// Warm both functions before the run: the flaky one must fail with its
	// scripted 500 (not a router-cache 404, which is permanent and would skip
	// the retry budget), and the recovery one must serve.
	f.Router(t).GetEventually(t, ctx, utils.UrlForFunction(failName, ns.Name),
		func(status int, body string) bool { return status == 500 && strings.Contains(body, "failed") })
	f.Router(t).GetEventually(t, ctx, utils.UrlForFunction(helloName, ns.Name), framework.BodyContains("hello"))

	wfName := "wf-retry-catch-" + ns.ID
	manifest := fmt.Sprintf(`
apiVersion: fission.io/v1
kind: Workflow
metadata:
  name: %[4]s
spec:
  startAt: flaky
  states:
    flaky:
      type: Task
      function: {name: %[1]s}
      retry: {maxAttempts: 2, backoffBase: 1s, backoffCap: 2s}
      catch:
        - {errorType: Fission.All, next: recover}
      next: never
    never: {type: Task, function: {name: %[2]s}, end: true}
    recover: {type: Task, function: {name: %[3]s}, end: true}
`, failName, helloName, helloName, wfName)
	createWorkflow(t, ctx, f, ns, wfName, manifest)

	out := ns.CLICaptureStdout(t, ctx, "workflow", "run", "--name", wfName)
	runName := startedRunName(t, out)

	runs := f.FissionClient().CoreV1().WorkflowRuns(ns.Name)
	run := waitForTerminalRun(t, ctx, runs, runName)

	require.Equal(t, fv1.WorkflowRunSucceeded, run.Status.Phase, "catch must route to recover")
	hist := ns.CLICaptureStdout(t, ctx, "workflow", "history", "--name", runName)
	assert.Equal(t, 2, strings.Count(hist, "StepFailed"), "both attempts recorded")
	assert.Contains(t, hist, "TimerFired")
}

// TestWorkflowRunAdmission pins the WorkflowRun webhook: the 256KiB input
// cap rejects oversized inputs; a small run referencing a nonexistent
// workflow is accepted (GitOps ordering) and just never progresses.
func TestWorkflowRunAdmission(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	runs := f.FissionClient().CoreV1().WorkflowRuns(ns.Name)

	_, err := runs.Create(ctx, &fv1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "oversized-" + ns.ID, Namespace: ns.Name},
		Spec: fv1.WorkflowRunSpec{
			WorkflowRef: "any",
			Input:       &apiextensionsv1.JSON{Raw: bytes.Repeat([]byte("x"), fv1.MaxWorkflowRunInputBytes+1)},
		},
	}, metav1.CreateOptions{})
	require.Error(t, err, "webhook must reject an oversized input")

	smallName := "small-" + ns.ID
	t.Cleanup(func() {
		_ = runs.Delete(context.Background(), smallName, metav1.DeleteOptions{})
	})
	created, err := runs.Create(ctx, &fv1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: smallName, Namespace: ns.Name},
		Spec: fv1.WorkflowRunSpec{
			WorkflowRef: "does-not-exist",
			Input:       &apiextensionsv1.JSON{Raw: []byte(`{"order":4711}`)},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "a dangling workflowRef is a controller condition, not an admission error")
	assert.False(t, created.Status.Phase.Terminal())

	// The spec is immutable after creation (cancel goes via annotation).
	created.Spec.WorkflowRef = "something-else"
	_, err = runs.Update(ctx, created, metav1.UpdateOptions{})
	require.Error(t, err, "spec mutation must be rejected at admission")
}
