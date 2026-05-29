// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fission/fission/pkg/utils/leaderelection"
)

func TestAwaitLeading(t *testing.T) {
	t.Run("returns true when leadership acquired", func(t *testing.T) {
		leading := make(chan struct{})
		close(leading)
		assert.True(t, awaitLeading(context.Background(), leading))
	})

	t.Run("returns false when ctx ends first", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		assert.False(t, awaitLeading(ctx, make(chan struct{})))
	})
}

func TestReadyzHandler(t *testing.T) {
	// An enabled-but-not-yet-leader elector (Run never called) reports
	// IsLeader() == false.
	notLeader := leaderelection.New(true, fake.NewSimpleClientset(), "ns", "lock", "id", logr.Discard())

	tests := []struct {
		name           string
		leaderElection bool
		elector        *leaderelection.Elector
		cachesSynced   bool
		want           int
	}{
		{"LE disabled and synced -> ready", false, nil, true, http.StatusOK},
		{"LE disabled and not synced -> 503", false, nil, false, http.StatusServiceUnavailable},
		{"LE enabled and not leader -> 503", true, notLeader, true, http.StatusServiceUnavailable},
		{"LE enabled and nil elector -> 503", true, nil, true, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Executor{leaderElection: tc.leaderElection, elector: tc.elector}
			e.cachesSynced.Store(tc.cachesSynced)

			rec := httptest.NewRecorder()
			e.readyzHandler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}
