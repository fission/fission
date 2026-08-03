// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func testPod(name string, ready bool, restarts int32, terminating bool) *apiv1.Pod {
	p := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fission"},
		Status: apiv1.PodStatus{
			Conditions: []apiv1.PodCondition{{
				Type:   apiv1.PodReady,
				Status: map[bool]apiv1.ConditionStatus{true: apiv1.ConditionTrue, false: apiv1.ConditionFalse}[ready],
			}},
			ContainerStatuses: []apiv1.ContainerStatus{{RestartCount: restarts}},
		},
	}
	if terminating {
		now := metav1.Now()
		p.DeletionTimestamp = &now
	}
	return p
}

// eventsOf drains the recorder's pod events into "name/event" strings.
func eventsOf(rec *upgradeRecorder) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, 0, len(rec.events))
	for _, e := range rec.events {
		out = append(out, e.Name+"/"+e.Event)
	}
	return out
}

// TestRecordPodTransition drives the pure transition logic through a pod's
// full lifecycle and asserts exactly the interesting events emerge: added,
// ready flips, restart increments, terminating, deleted — and nothing for
// no-op modifications.
func TestRecordPodTransition(t *testing.T) {
	t.Parallel()
	rec := newUpgradeRecorder(time.Now())
	states := map[string]*podState{}

	recordPodTransition(rec, "control-plane", watch.Added, testPod("router-1", false, 0, false), states)
	recordPodTransition(rec, "control-plane", watch.Modified, testPod("router-1", false, 0, false), states) // no-op
	recordPodTransition(rec, "control-plane", watch.Modified, testPod("router-1", true, 0, false), states)  // ready
	recordPodTransition(rec, "control-plane", watch.Modified, testPod("router-1", true, 1, false), states)  // restarted
	recordPodTransition(rec, "control-plane", watch.Modified, testPod("router-1", false, 1, true), states)  // not-ready + terminating
	recordPodTransition(rec, "control-plane", watch.Deleted, testPod("router-1", false, 1, true), states)

	assert.Equal(t, []string{
		"fission/router-1/added",
		"fission/router-1/ready",
		"fission/router-1/restarted",
		"fission/router-1/not-ready",
		"fission/router-1/terminating",
		"fission/router-1/deleted",
	}, eventsOf(rec))
	assert.Empty(t, states, "deleted pods must not leak state")
}

// TestRecordPodTransitionBornReady pins that a pod first seen already Ready
// emits both added and ready — the watcher can miss the intermediate states of
// a fast-starting pod, and "a new ready pod appeared" is exactly the signal
// the timeline needs around a roll.
func TestRecordPodTransitionBornReady(t *testing.T) {
	t.Parallel()
	rec := newUpgradeRecorder(time.Now())
	states := map[string]*podState{}
	recordPodTransition(rec, "function", watch.Added, testPod("pool-x", true, 0, false), states)
	require.Equal(t, []string{"fission/pool-x/added", "fission/pool-x/ready"}, eventsOf(rec))
}

// TestConsumePodEventsStopsOnClosedChannel pins the resilience contract: a
// closed watch channel returns (so the caller re-lists and re-watches) instead
// of spinning or panicking, and a watch.Error event does the same — that is
// the 410 Gone path.
func TestConsumePodEventsStopsOnClosedChannel(t *testing.T) {
	t.Parallel()
	rec := newUpgradeRecorder(time.Now())

	fw := watch.NewFake()
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumePodEvents(t.Context(), rec, "control-plane", fw, map[string]*podState{})
	}()
	fw.Add(testPod("executor-1", false, 0, false))
	fw.Stop() // closed channel -> consume returns
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumePodEvents did not return on a closed watch channel")
	}
	assert.Equal(t, []string{"fission/executor-1/added"}, eventsOf(rec))

	fw2 := watch.NewFake()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		consumePodEvents(t.Context(), rec, "control-plane", fw2, map[string]*podState{})
	}()
	go fw2.Error(&metav1.Status{Code: 410, Reason: metav1.StatusReasonExpired})
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("consumePodEvents did not return on a watch error event")
	}
}
