// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

// TestWorkflowRunAdmission pins the WorkflowRun webhook: the 256KiB input
// cap rejects oversized inputs, a small run is accepted and — with no
// engine in phase 1 — simply sits with an empty status.
func TestWorkflowRunAdmission(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
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
			WorkflowRef: "any",
			Input:       &runtime.RawExtension{Raw: []byte(`{"order":4711}`)},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	assert.Empty(t, created.Status.Phase, "no engine in phase 1: the run must sit un-serviced")
}
