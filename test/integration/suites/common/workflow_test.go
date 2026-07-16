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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

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

	dir := t.TempDir()
	path := filepath.Join(dir, "order-pipeline.yaml")
	require.NoError(t, os.WriteFile(path, []byte(orderPipelineManifest), 0o600))

	out := ns.CLI(t, ctx, "workflow", "create", "-f", path)
	assert.Contains(t, out, "created")

	// The mutating webhook defaulted the function-reference type.
	wf, err := f.FissionClient().CoreV1().Workflows(ns.Name).Get(ctx, "order-pipeline", metav1.GetOptions{})
	require.NoError(t, err)
	for name, st := range wf.Spec.States {
		require.NotNil(t, st.Function, "state %s", name)
		assert.EqualValues(t, fv1.FunctionReferenceTypeFunctionName, st.Function.Type, "state %s", name)
	}

	listOut := ns.CLICaptureStdout(t, ctx, "workflow", "list")
	assert.Contains(t, listOut, "order-pipeline")

	valOut := ns.CLICaptureStdout(t, ctx, "workflow", "validate", "-f", path, "--offline")
	assert.Contains(t, valOut, "valid")

	graphOut := ns.CLICaptureStdout(t, ctx, "workflow", "graph", "--name", "order-pipeline")
	assert.Contains(t, graphOut, "stateDiagram-v2")
	assert.Contains(t, graphOut, "validate --> charge")

	// The validating webhook rejects an unresolvable Next target.
	bad := strings.Replace(orderPipelineManifest, "next: charge", "next: ghost", 1)
	badPath := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(badPath, []byte(bad), 0o600))
	_, err = ns.CLICaptureStdoutBestEffort(t, ctx, "workflow", "create", "-f", badPath, "--name", "bad-pipeline")
	require.Error(t, err, "webhook must reject an unresolvable Next target")
	assert.Contains(t, err.Error(), "ghost")

	out = ns.CLI(t, ctx, "workflow", "delete", "--name", "order-pipeline")
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
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})

	manifest := fmt.Sprintf(`
apiVersion: fission.io/v1
kind: Workflow
metadata:
  name: wf-linear
spec:
  startAt: a
  states:
    a: {type: Task, function: {name: %[1]s}, next: b}
    b: {type: Task, function: {name: %[1]s}, next: c}
    c: {type: Task, function: {name: %[1]s}, end: true}
`, fnName)
	path := filepath.Join(t.TempDir(), "wf.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0o600))
	ns.CLI(t, ctx, "workflow", "create", "-f", path)

	out := ns.CLICaptureStdout(t, ctx, "workflow", "run", "--name", "wf-linear")
	require.Contains(t, out, "started")
	runName := strings.Trim(strings.Fields(strings.TrimSpace(out))[2], "'")

	runs := f.FissionClient().CoreV1().WorkflowRuns(ns.Name)
	var run *fv1.WorkflowRun
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		run, err = runs.Get(ctx, runName, metav1.GetOptions{})
		require.NoError(c, err)
		require.True(c, run.Status.Phase.Terminal(), "phase %s", run.Status.Phase)
	}, 3*time.Minute, 2*time.Second, "run must reach a terminal phase")

	require.Equal(t, fv1.WorkflowRunSucceeded, run.Status.Phase)
	require.NotNil(t, run.Status.Output)
	assert.Contains(t, string(run.Status.Output.Raw), "hello")
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

	manifest := fmt.Sprintf(`
apiVersion: fission.io/v1
kind: Workflow
metadata:
  name: wf-retry-catch
spec:
  startAt: flaky
  states:
    flaky:
      type: Task
      function: {name: %s}
      retry: {maxAttempts: 2, backoffBase: 1s, backoffCap: 2s}
      catch:
        - {errorType: Fission.All, next: recover}
      next: never
    never: {type: Task, function: {name: %s}, end: true}
    recover: {type: Task, function: {name: %s}, end: true}
`, failName, helloName, helloName)
	path := filepath.Join(t.TempDir(), "wf.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0o600))
	ns.CLI(t, ctx, "workflow", "create", "-f", path)

	out := ns.CLICaptureStdout(t, ctx, "workflow", "run", "--name", "wf-retry-catch")
	runName := strings.Trim(strings.Fields(strings.TrimSpace(out))[2], "'")

	runs := f.FissionClient().CoreV1().WorkflowRuns(ns.Name)
	var run *fv1.WorkflowRun
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		run, err = runs.Get(ctx, runName, metav1.GetOptions{})
		require.NoError(c, err)
		require.True(c, run.Status.Phase.Terminal(), "phase %s", run.Status.Phase)
	}, 3*time.Minute, 2*time.Second)

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
		ObjectMeta: metav1.ObjectMeta{Name: "oversized", Namespace: ns.Name},
		Spec: fv1.WorkflowRunSpec{
			WorkflowRef: "any",
			Input:       &runtime.RawExtension{Raw: bytes.Repeat([]byte("x"), fv1.MaxWorkflowRunInputBytes+1)},
		},
	}, metav1.CreateOptions{})
	require.Error(t, err, "webhook must reject an oversized input")

	created, err := runs.Create(ctx, &fv1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "small", Namespace: ns.Name},
		Spec: fv1.WorkflowRunSpec{
			WorkflowRef: "does-not-exist",
			Input:       &runtime.RawExtension{Raw: []byte(`{"order":4711}`)},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "a dangling workflowRef is a controller condition, not an admission error")
	assert.False(t, created.Status.Phase.Terminal())

	// The spec is immutable after creation (cancel goes via annotation).
	created.Spec.WorkflowRef = "something-else"
	_, err = runs.Update(ctx, created, metav1.UpdateOptions{})
	require.Error(t, err, "spec mutation must be rejected at admission")
}
