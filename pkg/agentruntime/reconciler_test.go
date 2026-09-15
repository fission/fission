// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func newTestReconciler(t *testing.T, objs ...client.Object) (*AgentReconciler, *AgentView, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		Build()
	view := NewAgentView()
	r := NewAgentReconciler(logr.Discard(), c, view, 0)
	return r, view, c
}

func agentFn(name string, agent *fv1.AgentConfig) *fv1.Function {
	return &fv1.Function{
		Name: name, Namespace: "default", Generation: 1,
		Spec: fv1.FunctionSpec{Agent: agent},
	}
}

func TestAgentReconcilerBuildsEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		agent *fv1.AgentConfig
		want  AgentEntry
	}{
		{
			name:  "all nil/zero applies platform defaults",
			agent: &fv1.AgentConfig{},
			want: AgentEntry{
				SessionSource: fv1.SessionSourceHeader,
				SessionName:   fv1.DefaultAgentSessionHeader,
				IdleAfter:     DefaultIdleAfter,
				ArchiveAfter:  DefaultArchiveAfter,
				MaxSessions:   0,
			},
		},
		{
			name: "explicit values carry through",
			agent: &fv1.AgentConfig{
				Session:      &fv1.AgentSessionConfig{Source: fv1.SessionSourceQueryParam, Name: "session"},
				IdleAfter:    &metav1.Duration{Duration: 10 * time.Minute},
				ArchiveAfter: &metav1.Duration{Duration: 48 * time.Hour},
				MaxSessions:  42,
			},
			want: AgentEntry{
				SessionSource: fv1.SessionSourceQueryParam,
				SessionName:   "session",
				IdleAfter:     10 * time.Minute,
				ArchiveAfter:  48 * time.Hour,
				MaxSessions:   42,
			},
		},
		{
			name: "explicit zero durations also apply platform defaults",
			agent: &fv1.AgentConfig{
				IdleAfter:    &metav1.Duration{Duration: 0},
				ArchiveAfter: &metav1.Duration{Duration: 0},
			},
			want: AgentEntry{
				SessionSource: fv1.SessionSourceHeader,
				SessionName:   fv1.DefaultAgentSessionHeader,
				IdleAfter:     DefaultIdleAfter,
				ArchiveAfter:  DefaultArchiveAfter,
				MaxSessions:   0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fn := agentFn("agent-fn", tt.agent)
			r, view, _ := newTestReconciler(t, fn)
			key := types.NamespacedName{Namespace: "default", Name: "agent-fn"}

			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)

			e, ok := view.Lookup("default", "agent-fn")
			require.True(t, ok, "entry should be in the view")
			tt.want.Namespace = "default"
			tt.want.Name = "agent-fn"
			assert.Equal(t, tt.want, e)
		})
	}
}

func TestAgentReconcilerAgentNilRemoves(t *testing.T) {
	t.Parallel()
	fn := agentFn("no-agent", nil)
	r, view, _ := newTestReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "no-agent"}

	// Seed the view as if a prior reconcile had exposed it, to prove Agent==nil
	// removes an existing entry rather than just skipping insertion.
	view.Upsert(AgentEntry{Namespace: "default", Name: "no-agent"})

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	_, ok := view.Lookup("default", "no-agent")
	assert.False(t, ok, "a function without Agent must not be in the view")
}

func TestAgentReconcilerNotFoundRemoves(t *testing.T) {
	t.Parallel()
	r, view, _ := newTestReconciler(t)
	key := types.NamespacedName{Namespace: "default", Name: "missing"}

	view.Upsert(AgentEntry{Namespace: "default", Name: "missing"})

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	_, ok := view.Lookup("default", "missing")
	assert.False(t, ok, "a deleted function must be removed from the view")
}

func TestAgentReconcilerUpdatesExistingEntry(t *testing.T) {
	t.Parallel()
	fn := agentFn("updatable", &fv1.AgentConfig{MaxSessions: 5})
	r, view, c := newTestReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "updatable"}
	ctx := t.Context()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	before, ok := view.Lookup("default", "updatable")
	require.True(t, ok)
	require.Equal(t, DefaultIdleAfter, before.IdleAfter)
	require.EqualValues(t, 5, before.MaxSessions)

	got := &fv1.Function{}
	require.NoError(t, c.Get(ctx, key, got))
	got.Spec.Agent.IdleAfter = &metav1.Duration{Duration: 30 * time.Minute}
	got.Spec.Agent.MaxSessions = 99
	require.NoError(t, c.Update(ctx, got))

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	after, ok := view.Lookup("default", "updatable")
	require.True(t, ok)
	assert.Equal(t, 30*time.Minute, after.IdleAfter, "reconcile must pick up the updated spec, not keep serving the stale entry")
	assert.EqualValues(t, 99, after.MaxSessions)
}

func TestAgentReconcilerIdempotent(t *testing.T) {
	t.Parallel()
	fn := agentFn("steady", &fv1.AgentConfig{MaxSessions: 5})
	r, view, _ := newTestReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "steady"}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	first, ok := view.Lookup("default", "steady")
	require.True(t, ok)

	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	second, ok := view.Lookup("default", "steady")
	require.True(t, ok)

	assert.Equal(t, first, second, "re-reconciling an unchanged function must be a no-op on the entry")
	assert.Len(t, view.List(), 1)
}

// TestAgentReconcilerSkipsInvalidStoredConfig pins fix #11: a stored
// AgentConfig that fails Validate (here an empty Session.Name, which would make
// every turn mint a brand-new session) must never reach the view — the
// reconciler validates before Upsert and removes it instead.
func TestAgentReconcilerSkipsInvalidStoredConfig(t *testing.T) {
	t.Parallel()
	fn := agentFn("bad", &fv1.AgentConfig{
		Session: &fv1.AgentSessionConfig{Source: fv1.SessionSourceHeader, Name: ""},
	})
	r, view, _ := newTestReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "bad"}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err, "an invalid stored config is dropped, not surfaced as a reconcile error")

	_, ok := view.Lookup("default", "bad")
	assert.False(t, ok, "an agent with an invalid stored config must not be served from the view")
}

// TestAgentReconcilerStateKeyspace pins the end-to-end G13 plumbing: the
// reconciler resolves Spec.State into AgentEntry.StateKeyspace via
// StateConfig.EffectiveKeyspace, and carries HistoryTrimBelowCheckpoint
// through as HistoryTrim. A function with no State resolves to an empty
// keyspace, the "history disabled" signal Tasks 4-5 key off of.
func TestAgentReconcilerStateKeyspace(t *testing.T) {
	t.Parallel()

	t.Run("no State -> empty keyspace", func(t *testing.T) {
		t.Parallel()
		fn := agentFn("no-state", &fv1.AgentConfig{})
		r, view, _ := newTestReconciler(t, fn)
		key := types.NamespacedName{Namespace: "default", Name: "no-state"}

		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
		require.NoError(t, err)

		e, ok := view.Lookup("default", "no-state")
		require.True(t, ok)
		assert.Empty(t, e.StateKeyspace)
		assert.False(t, e.HistoryTrim)
	})

	t.Run("State + HistoryTrimBelowCheckpoint carried through", func(t *testing.T) {
		t.Parallel()
		fn := &fv1.Function{
			Name: "with-state", Namespace: "default", Generation: 1,
			Spec: fv1.FunctionSpec{
				Agent: &fv1.AgentConfig{HistoryTrimBelowCheckpoint: true},
				State: &fv1.StateConfig{Keyspace: "custom-ks"},
			},
		}
		r, view, _ := newTestReconciler(t, fn)
		key := types.NamespacedName{Namespace: "default", Name: "with-state"}

		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
		require.NoError(t, err)

		e, ok := view.Lookup("default", "with-state")
		require.True(t, ok)
		assert.Equal(t, "custom-ks", e.StateKeyspace)
		assert.True(t, e.HistoryTrim)
	})
}
