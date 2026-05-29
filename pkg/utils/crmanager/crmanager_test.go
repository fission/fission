// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package crmanager

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestLeaderRunnable(t *testing.T) {
	ran := false
	r := LeaderRunnable(func(context.Context) error {
		ran = true
		return nil
	})
	assert.True(t, r.NeedLeaderElection(), "runnable must be leader-only")
	require.NoError(t, r.Start(context.Background()))
	assert.True(t, ran, "Start must invoke the wrapped function")
}

func TestNewLeaderElectedDisabled(t *testing.T) {
	// With election disabled the Manager is built without contacting the API
	// server (no Start), so a dummy rest.Config is sufficient. Pin
	// LEADER_ELECTION_ENABLED so the test is deterministic regardless of the
	// ambient environment.
	t.Setenv("LEADER_ELECTION_ENABLED", "false")
	mgr, err := NewLeaderElected(&rest.Config{Host: "http://127.0.0.1:1"}, "test-lock", logr.Discard())
	require.NoError(t, err)
	assert.NotNil(t, mgr)
}

func TestNewLeaderElectedAutoDetectsNamespace(t *testing.T) {
	// With election enabled the Manager auto-detects the lease namespace from
	// the in-cluster service-account mount (we deliberately don't set
	// LeaderElectionNamespace). Running out-of-cluster, that detection fails
	// fast at construction — confirming the auto-detect path is wired and that
	// callers don't need to plumb a namespace in-cluster.
	t.Setenv("LEADER_ELECTION_ENABLED", "true")
	_, err := NewLeaderElected(&rest.Config{Host: "http://127.0.0.1:1"}, "test-lock", logr.Discard())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leader election namespace")
}
