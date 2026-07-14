// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// mkPod is a compact builder for the pod shapes the filters care about:
// phase, PodIP, DeletionTimestamp, and a slice of (Ready, Name) container
// statuses. Everything else is left zero.
func mkPod(phase corev1.PodPhase, ip string, deleting bool, containers ...corev1.ContainerStatus) *corev1.Pod {
	p := &corev1.Pod{Status: corev1.PodStatus{
		Phase:             phase,
		PodIP:             ip,
		ContainerStatuses: containers,
	}}
	if deleting {
		p.DeletionTimestamp = &metav1.Time{}
	}
	return p
}

func readyCs(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: true}
}

func notReadyCs(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: false}
}

// ---------------------------------------------------------------------------
// IsReadyPod
// ---------------------------------------------------------------------------

func TestIsReadyPod(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"nil pod", nil, false},
		{"running, ip, all containers ready", mkPod(corev1.PodRunning, "10.0.0.1", false, readyCs("a")), true},
		{"running, ip, no containers", mkPod(corev1.PodRunning, "10.0.0.1", false), true},
		{"running, no ip", mkPod(corev1.PodRunning, "", false, readyCs("a")), false},
		{"running, ip, one container not ready", mkPod(corev1.PodRunning, "10.0.0.1", false, readyCs("a"), notReadyCs("b")), false},
		{"running, ip, all containers not ready", mkPod(corev1.PodRunning, "10.0.0.1", false, notReadyCs("a")), false},
		{"deleting", mkPod(corev1.PodRunning, "10.0.0.1", true, readyCs("a")), false},
		{"pending, ip, ready", mkPod(corev1.PodPending, "10.0.0.1", false, readyCs("a")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsReadyPod(tt.pod))
		})
	}
}

// ---------------------------------------------------------------------------
// IsPodRunning
// ---------------------------------------------------------------------------

func TestIsPodRunning(t *testing.T) {
	tests := []struct {
		name  string
		phase corev1.PodPhase
		want  bool
	}{
		{"running", corev1.PodRunning, true},
		{"pending", corev1.PodPending, false},
		{"succeeded", corev1.PodSucceeded, false},
		{"failed", corev1.PodFailed, false},
		{"unknown", corev1.PodUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{Phase: tt.phase}}
			assert.Equal(t, tt.want, IsPodRunning(pod))
		})
	}
}

// ---------------------------------------------------------------------------
// IsPodTerminated
// ---------------------------------------------------------------------------

func TestIsPodTerminated(t *testing.T) {
	tests := []struct {
		name  string
		phase corev1.PodPhase
		want  bool
	}{
		{"pending is not terminated", corev1.PodPending, false},
		{"running is not terminated", corev1.PodRunning, false},
		{"unknown is not terminated", corev1.PodUnknown, false},
		{"succeeded is terminated", corev1.PodSucceeded, true},
		{"failed is terminated", corev1.PodFailed, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{Phase: tt.phase}}
			assert.Equal(t, tt.want, IsPodTerminated(pod))
		})
	}
}

// ---------------------------------------------------------------------------
// PodContainerReadyStatus
// ---------------------------------------------------------------------------

func TestPodContainerReadyStatus(t *testing.T) {
	t.Run("nil container statuses", func(t *testing.T) {
		pod := &corev1.Pod{}
		ready, total := PodContainerReadyStatus(pod)
		assert.Equal(t, 0, ready)
		assert.Equal(t, 0, total)
	})

	t.Run("all ready", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{readyCs("a"), readyCs("b"), readyCs("c")},
		}}
		ready, total := PodContainerReadyStatus(pod)
		assert.Equal(t, 3, ready)
		assert.Equal(t, 3, total)
	})

	t.Run("mixed", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{readyCs("a"), notReadyCs("b"), readyCs("c")},
		}}
		ready, total := PodContainerReadyStatus(pod)
		assert.Equal(t, 2, ready)
		assert.Equal(t, 3, total)
	})

	t.Run("none ready", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{notReadyCs("a"), notReadyCs("b")},
		}}
		ready, total := PodContainerReadyStatus(pod)
		assert.Equal(t, 0, ready)
		assert.Equal(t, 2, total)
	})
}

// ---------------------------------------------------------------------------
// ReadyAndRunningPodsFilter
// ---------------------------------------------------------------------------

func TestReadyAndRunningPodsFilter(t *testing.T) {
	// Build a mix: only pod "good" is both ready AND running.
	pods := []corev1.Pod{
		// ready + running
		*mkPod(corev1.PodRunning, "10.0.0.1", false, readyCs("fn")),
		// running but not ready (no ip)
		*mkPod(corev1.PodRunning, "", false, readyCs("fn")),
		// ready but not running (pending)
		*mkPod(corev1.PodPending, "10.0.0.2", false, readyCs("fn")),
		// running + ip but container not ready
		*mkPod(corev1.PodRunning, "10.0.0.3", false, notReadyCs("fn")),
		// running + ip + ready but deleting
		*mkPod(corev1.PodRunning, "10.0.0.4", true, readyCs("fn")),
		// succeeded (terminated)
		*mkPod(corev1.PodSucceeded, "10.0.0.5", false, readyCs("fn")),
	}
	list := &corev1.PodList{Items: pods}
	got := ReadyAndRunningPodsFilter(list)
	require.Len(t, got, 1, "only the first pod is ready+running")
	assert.Equal(t, corev1.PodRunning, got[0].Status.Phase)
	assert.Equal(t, "10.0.0.1", got[0].Status.PodIP)

	t.Run("empty list", func(t *testing.T) {
		got := ReadyAndRunningPodsFilter(&corev1.PodList{})
		assert.Empty(t, got)
	})

	t.Run("nil items", func(t *testing.T) {
		got := ReadyAndRunningPodsFilter(&corev1.PodList{Items: nil})
		assert.Empty(t, got, "filter returns empty slice, not nil")
	})
}
