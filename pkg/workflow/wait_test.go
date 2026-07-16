// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore/memory"
)

// waitSpec pauses six hours between start and done — no functions, so the
// whole engine runs inside a synctest bubble (no real network).
func waitSpec() *fv1.WorkflowSpec {
	return &fv1.WorkflowSpec{
		StartAt: "pause",
		States: map[string]fv1.WorkflowState{
			"pause": {Type: fv1.WorkflowStateWait, Duration: &metav1.Duration{Duration: 6 * time.Hour}, Next: "done"},
			"done":  {Type: fv1.WorkflowStateSucceed},
		},
	}
}

// TestWaitStateVirtualTime is the RFC's "wait 6h then resume completes in
// microseconds" verification: the engine uses the standard time package (no
// clock seam), the memory statestore's queue delay is real time arithmetic,
// and the synctest bubble advances the clock whenever every goroutine is
// idle. No sleeps, no flakes.
func TestWaitStateVirtualTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		caps, err := memory.New()
		require.NoError(t, err)
		defer func() { _ = caps.Close() }()
		el, _ := caps.EventLog()
		q, _ := caps.Queue()
		kv, _ := caps.KV()

		engine := NewEngine(EngineOptions{
			Logger: logr.Discard(), EventLog: el, Queue: q, KV: kv,
			Invoker: NewInvoker(InvokerOptions{Logger: logr.Discard(), EventLog: el, KV: kv, Wake: func(types.NamespacedName) {}}),
			Wake:    func(types.NamespacedName) {},
			Rand:    func() float64 { return 0.5 },
		})

		run := &fv1.WorkflowRun{
			ObjectMeta: metav1.ObjectMeta{Name: "waiter", Namespace: "default", UID: types.UID("uid-waiter")},
			Spec:       fv1.WorkflowRunSpec{WorkflowRef: "wf", Input: &apiextensionsv1.JSON{Raw: []byte(`{"n":1}`)}},
		}
		fetch := func(context.Context) (*fv1.WorkflowSpec, error) { return waitSpec(), nil }

		started := time.Now()
		var s *RunState
		for range 100 {
			engine.timerPollOnce(t.Context())
			s, err = engine.Reconcile(t.Context(), run, fetch)
			require.NoError(t, err)
			if s.Terminal != "" {
				break
			}
			time.Sleep(30 * time.Minute) // virtual: advances instantly when idle
		}

		require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
		assert.JSONEq(t, `{"n":1}`, string(s.Output), "a wait passes its input through")
		elapsed := time.Since(started)
		assert.GreaterOrEqual(t, elapsed, 6*time.Hour, "virtual clock advanced through the wait")
		assert.Less(t, elapsed, 7*time.Hour)
	})
}
