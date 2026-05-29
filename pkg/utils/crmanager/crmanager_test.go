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

func TestNewLeaderElected(t *testing.T) {
	// LEADER_ELECTION_ENABLED unset -> election disabled; the Manager is built
	// without contacting the API server (no Start), so a dummy rest.Config is
	// sufficient to exercise construction.
	mgr, err := NewLeaderElected(&rest.Config{Host: "http://127.0.0.1:1"}, "test-lock", logr.Discard())
	require.NoError(t, err)
	assert.NotNil(t, mgr)
}
