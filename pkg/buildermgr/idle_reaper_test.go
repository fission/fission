// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// recordingScaler captures scale calls so the reaper's decisions can be asserted
// without relying on the client-go fake's (unimplemented) scale subresource.
type recordingScaler struct {
	mu    sync.Mutex
	calls map[string]int32 // "ns/name" -> last requested replicas
}

func newRecordingScaler() *recordingScaler {
	return &recordingScaler{calls: map[string]int32{}}
}

func (s *recordingScaler) scale(_ context.Context, ns, name string, replicas int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[ns+"/"+name] = replicas
	return nil
}

func TestIdleReaperScalesIdleBuildersToZero(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	m := newBuilderPoolManager(loggerfactory.GetLogger())
	m.now = func() time.Time { return now }

	idle := poolEnv("idle", "idle", "1", i64(60), nil)
	m.Ensure(idle, "default")
	busy := poolEnv("busy", "busy", "1", i64(60), nil)
	m.StartBuild(busy, "default", poolPkg("p")) // in-flight -> protected from reaping

	now = now.Add(120 * time.Second) // past the 60s idle window

	scaler := newRecordingScaler()
	r := newIdleBuilderReaper(loggerfactory.GetLogger(), m, scaler.scale, time.Minute)
	r.reap(t.Context())

	scaler.mu.Lock()
	idleReplicas, idleScaled := scaler.calls["default/idle-1"]
	_, busyScaled := scaler.calls["default/busy-1"]
	scaler.mu.Unlock()

	assert.True(t, idleScaled, "idle builder past its timeout must be scaled")
	assert.Equal(t, int32(0), idleReplicas, "idle builder must be scaled to zero")
	assert.False(t, busyScaled, "a building env's builder must never be scaled down")

	// After reaping, the env is flagged scaledToZero and must not be re-targeted
	// until a new build clears the flag.
	assert.Empty(t, m.ReapTargets(), "reaped env must not be a target again until a new build")
}
