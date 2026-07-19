// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/statestore"
)

func TestCleanupRunReclaimsStreamAndKV(t *testing.T) {
	t.Parallel()

	h := newHarness(t, pipelineSpec())
	s := h.drive(t, h.engine, 10*time.Second)
	require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)

	// Seed a spill + checkpoint entry so the cleanup has something to do.
	_, err := spill(t.Context(), h.kv, h.run.Namespace, h.run.Name, "x", 1, []byte(`"blob"`))
	require.NoError(t, err)

	require.NoError(t, h.engine.CleanupRun(t.Context(), h.run.Namespace, h.run.Name, h.run.UID))

	events, err := h.el.Read(t.Context(), streamName(h.run), 0, 100)
	require.NoError(t, err)
	assert.Empty(t, events, "every event payload reclaimed")

	for _, scope := range []statestore.Scope{
		ioScope(h.run.Namespace, h.run.Name),
		checkpointScope(h.run.Namespace, h.run.Name),
	} {
		page, err := h.kv.List(t.Context(), scope, "", statestore.Page{})
		require.NoError(t, err)
		assert.Empty(t, page.Keys, "keyspace %s reclaimed", scope.Keyspace)
	}
}

func TestRetentionSweeper(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	old := metav1.NewTime(now.Add(-2 * time.Hour))

	wf := &fv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "default"},
		Spec: fv1.WorkflowSpec{
			StartAt: "a",
			States:  map[string]fv1.WorkflowState{"a": {Type: fv1.WorkflowStateSucceed}},
			HistoryRetention: &fv1.WorkflowRetentionPolicy{
				MaxCount: new(int32(1)),
			},
		},
	}
	run := func(name string, finished metav1.Time) *fv1.WorkflowRun {
		return &fv1.WorkflowRun{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default", UID: types.UID("uid-" + name),
				Finalizers: []string{FinalizerName},
			},
			Spec: fv1.WorkflowRunSpec{WorkflowRef: "wf"},
			Status: fv1.WorkflowRunStatus{
				Phase: fv1.WorkflowRunSucceeded, FinishedAt: &finished,
			},
		}
	}

	fc := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(wf, run("newest", now), run("older", old)).
		WithIndex(&fv1.WorkflowRun{}, WorkflowRefIndex, func(obj client.Object) []string {
			return []string{obj.(*fv1.WorkflowRun).Spec.WorkflowRef}
		}).
		Build()

	h := newHarness(t, pipelineSpec())
	rs := &RetentionSweeper{client: fc, engine: h.engine}
	rs.sweep(t.Context())

	var runs fv1.WorkflowRunList
	require.NoError(t, fc.List(t.Context(), &runs))
	names := map[string]bool{}
	for _, r := range runs.Items {
		names[r.Name] = true
		// The fake client leaves finalizer-bearing objects in Terminating;
		// deletion was REQUESTED for the older run.
		if r.Name == "older" {
			assert.NotNil(t, r.DeletionTimestamp, "older run deleted by MaxCount=1")
		}
	}
	assert.True(t, names["newest"], "newest kept")
}
