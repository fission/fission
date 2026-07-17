// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package serial_test holds tests that mutate cluster-wide control-plane
// state; this one restarts the workflow Deployment mid-run.
package serial_test

import (
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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/test/integration/framework"
)

// TestWorkflowResumeAcrossRestart is the RFC's resume-on-restart proof on a
// live cluster: a run pauses in a durable Wait while the workflow controller
// is restarted; the run must complete afterwards WITHOUT re-executing the
// already-completed step (W1 asserted from the history: every (state,
// attempt) scheduled exactly once across the restart).
func TestWorkflowResumeAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	envName := "nodejs-wfres-" + ns.ID
	fnName := "hello-wfres-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	// wf-step returns a JSON object — the second Task receives the first's
	// output through the Wait state, and the node env's strict body-parser
	// rejects a bare-string body.
	codePath := framework.WriteTestData(t, "nodejs/wf-step/wf-step.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	// Warm the function: the run's single default attempt must not be spent
	// on a router-cache 404 or cold-start hiccup.
	f.Router(t).GetEventually(t, ctx, utils.UrlForFunction(fnName, ns.Name), framework.BodyContains("hops"))

	wfName := "wf-resume-" + ns.ID
	manifest := fmt.Sprintf(`
apiVersion: fission.io/v1
kind: Workflow
metadata:
  name: %[2]s
spec:
  startAt: before
  states:
    before: {type: Task, function: {name: %[1]s}, next: pause}
    pause:  {type: Wait, duration: 45s, next: after}
    after:  {type: Task, function: {name: %[1]s}, end: true}
`, fnName, wfName)
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

	out := ns.CLICaptureStdout(t, ctx, "workflow", "run", "--name", wfName)
	m := regexp.MustCompile(`workflow run '([^']+)' started`).FindStringSubmatch(out)
	require.NotNilf(t, m, "no started line in output:\n%s", out)
	runName := m[1]
	runs := f.FissionClient().CoreV1().WorkflowRuns(ns.Name)

	// Wait until the run is parked in the Wait state (step 1 done, timer
	// armed), then restart the controller under it.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		run, err := runs.Get(ctx, runName, metav1.GetOptions{})
		require.NoError(c, err)
		require.Contains(c, run.Status.ActiveStates, "pause", "run must be parked in the Wait state")
	}, 2*time.Minute, 2*time.Second)

	gen := f.RestartDeployment(t, ctx, "workflow")
	f.WaitForDeploymentRollout(t, ctx, "workflow", gen, 4*time.Minute)

	var run *fv1.WorkflowRun
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		run, err = runs.Get(ctx, runName, metav1.GetOptions{})
		require.NoError(c, err)
		require.True(c, run.Status.Phase.Terminal(), "phase %s", run.Status.Phase)
	}, 3*time.Minute, 2*time.Second, "the restarted controller must resume and finish the run")

	require.Equal(t, fv1.WorkflowRunSucceeded, run.Status.Phase)
	assert.True(t, meta.IsStatusConditionTrue(run.Status.Conditions, fv1.WorkflowRunConditionAccepted))

	// W1 across the restart: each step scheduled exactly once — the resume
	// must not have re-executed the pre-restart step.
	hist := ns.CLICaptureStdout(t, ctx, "workflow", "runs", "history", "--name", runName)
	assert.Equal(t, 2, strings.Count(hist, "StepScheduled"), "before+after, once each:\n%s", hist)
	assert.Equal(t, 1, strings.Count(hist, "TimerFired"), "the durable wait fired once:\n%s", hist)
}
