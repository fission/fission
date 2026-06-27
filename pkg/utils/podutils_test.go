// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// readyPod returns a minimal Pod that IsReadyPod considers ready: it has an IP
// and all of its containers report Ready. Individual cases mutate the returned
// pod to exercise the negative branches.
func readyPod() *v1.Pod {
	return &v1.Pod{
		Status: v1.PodStatus{
			PodIP: "10.0.0.1",
			ContainerStatuses: []v1.ContainerStatus{
				{Ready: true},
				{Ready: true},
			},
		},
	}
}

func TestIsReadyPod(t *testing.T) {
	t.Parallel()

	deleting := readyPod()
	deleting.DeletionTimestamp = &metav1.Time{}

	noIP := readyPod()
	noIP.Status.PodIP = ""

	notReadyContainer := readyPod()
	notReadyContainer.Status.ContainerStatuses[1].Ready = false

	noContainers := readyPod()
	noContainers.Status.ContainerStatuses = nil

	for _, tt := range []struct {
		name string
		pod  *v1.Pod
		want bool
	}{
		{name: "nil pod is not ready", pod: nil, want: false},
		{name: "terminating pod is not ready", pod: deleting, want: false},
		{name: "pod without an IP is not ready", pod: noIP, want: false},
		{name: "pod with a not-ready container is not ready", pod: notReadyContainer, want: false},
		{name: "pod with IP and no containers is ready", pod: noContainers, want: true},
		{name: "pod with IP and all containers ready is ready", pod: readyPod(), want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsReadyPod(tt.pod))
		})
	}
}

func TestIsPodTerminated(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		phase v1.PodPhase
		want  bool
	}{
		{name: "pending is not terminated", phase: v1.PodPending, want: false},
		{name: "running is not terminated", phase: v1.PodRunning, want: false},
		{name: "unknown is not terminated", phase: v1.PodUnknown, want: false},
		{name: "succeeded is terminated", phase: v1.PodSucceeded, want: true},
		{name: "failed is terminated", phase: v1.PodFailed, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pod := &v1.Pod{Status: v1.PodStatus{Phase: tt.phase}}
			assert.Equal(t, tt.want, IsPodTerminated(pod))
		})
	}
}

func TestPodContainerReadyStatus(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		statuses  []v1.ContainerStatus
		wantReady int
		wantTotal int
	}{
		{
			name:      "no containers",
			statuses:  nil,
			wantReady: 0,
			wantTotal: 0,
		},
		{
			name:      "all containers ready",
			statuses:  []v1.ContainerStatus{{Ready: true}, {Ready: true}},
			wantReady: 2,
			wantTotal: 2,
		},
		{
			name:      "some containers ready",
			statuses:  []v1.ContainerStatus{{Ready: true}, {Ready: false}, {Ready: true}},
			wantReady: 2,
			wantTotal: 3,
		},
		{
			name:      "no containers ready",
			statuses:  []v1.ContainerStatus{{Ready: false}, {Ready: false}},
			wantReady: 0,
			wantTotal: 2,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: tt.statuses}}
			ready, total := PodContainerReadyStatus(pod)
			assert.Equal(t, tt.wantReady, ready, "ready containers")
			assert.Equal(t, tt.wantTotal, total, "total containers")
		})
	}
}
