// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestWait_FunctionCondition exercises the `fission fn wait --for=condition=...`
// command end to end against the apiserver. It writes a uniquely-typed smoke
// condition via UpdateStatus (so the assertion stays stable regardless of which
// conditions controllers populate), then verifies:
//   - wait returns success once the condition holds the requested status, and
//   - wait fails (non-zero exit) when the requested status is never reached.
//
// It reuses minimalFunction / smokeConditionType from conditions_test.go.
func TestWait_FunctionCondition(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	name := "wait-fn-" + ns.ID
	_, err := fc.Functions(ns.Name).Create(ctx, minimalFunction(name, ns.Name), metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = fc.Functions(ns.Name).Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	// Drive the smoke condition to True via the status subresource.
	updateFunctionStatusWithRetry(ctx, t, f, ns.Name, name, func(fn *fv1.Function) {
		fn.Status.Conditions = append(fn.Status.Conditions, metav1.Condition{
			Type:               smokeConditionType,
			Status:             metav1.ConditionTrue,
			Reason:             "WaitSmoke",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: fn.Generation,
		})
	})

	// Already True → wait returns success and prints the "condition met" line.
	out := ns.CLICaptureStdout(t, ctx, "fn", "wait", "--name", name,
		"--for=condition="+smokeConditionType, "--timeout=15s")
	require.Contains(t, out, "condition met")

	// Requesting a status the condition never reaches must time out (error).
	_, err = ns.CLICaptureStdoutBestEffort(t, ctx, "fn", "wait", "--name", name,
		"--for=condition="+smokeConditionType+"=False", "--timeout=2s")
	require.Error(t, err, "wait for an unreached condition status must fail")
}
