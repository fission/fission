// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadyzHandler(t *testing.T) {
	tests := []struct {
		name           string
		leaderElection bool
		isLeader       bool
		cachesSynced   bool
		want           int
	}{
		{"LE disabled and synced -> ready", false, false, true, http.StatusOK},
		{"LE disabled and not synced -> 503", false, false, false, http.StatusServiceUnavailable},
		{"LE enabled, leader, synced -> ready", true, true, true, http.StatusOK},
		{"LE enabled and not leader -> 503", true, false, true, http.StatusServiceUnavailable},
		{"LE enabled, leader, not synced -> 503", true, true, false, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Executor{leaderElection: tc.leaderElection}
			e.isLeader.Store(tc.isLeader)
			e.cachesSynced.Store(tc.cachesSynced)

			rec := httptest.NewRecorder()
			e.readyzHandler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestRunnableLeaderElection(t *testing.T) {
	assert.True(t, (&executorControllers{}).NeedLeaderElection(),
		"controllers must run on the leader only")
	assert.False(t, (&executorAPIServer{}).NeedLeaderElection(),
		"the API server must run on every replica so /readyz answers everywhere")
}
