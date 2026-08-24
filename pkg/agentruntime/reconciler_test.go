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
	r := NewAgentReconciler(logr.Discard(), c, view)
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
