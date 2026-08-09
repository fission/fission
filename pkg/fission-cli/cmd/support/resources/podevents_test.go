// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func fissionPod(name string, uid types.UID) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "fission", UID: uid, ResourceVersion: "1",
			Labels: map[string]string{"svc": "router"},
		},
	}
}

// podEvent builds a legacy core/v1 event, as k8s.io/client-go/tools/record
// writes them — kubelet's Pulled, Unhealthy, OOMKilled and friends.
func podEvent(name string, uid types.UID, reason string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name + "." + reason, Namespace: "fission"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: name, Namespace: "fission", UID: uid,
		},
		Reason:        reason,
		Message:       reason + " happened",
		LastTimestamp: metav1.NewTime(at),
	}
}

// schedulerEvent builds an events.k8s.io/v1 event as it reads back through the
// core/v1 endpoint: EventTime set, LastTimestamp zero. This is what
// k8s.io/client-go/tools/events produces, and therefore what kube-scheduler's
// FailedScheduling looks like to this dumper.
func schedulerEvent(name string, uid types.UID, reason string, at time.Time) *corev1.Event {
	e := podEvent(name, uid, reason, at)
	e.LastTimestamp = metav1.Time{}
	e.EventTime = metav1.NewMicroTime(at)
	return e
}

func TestPodEventDumperWritesEventsForMatchingPods(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	pod := fissionPod("router-abc", "uid-router")

	// A pod that does not match the selector, with events of its own.
	other := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unrelated", Namespace: "default", UID: "uid-other", ResourceVersion: "1",
			Labels: map[string]string{"app": "someone-else"},
		},
	}

	// Reasons are named so that alphabetical order is the exact reverse of
	// chronological order. The fake clientset returns list results name-sorted,
	// so with chronological names an absent sort would still look correct.
	// FailedScheduling sits in the middle and carries only EventTime, so a
	// comparator that reads LastTimestamp alone drops it to the front.
	client := k8sfake.NewClientset(
		pod, other,
		podEvent("router-abc", "uid-router", "Zulu", base),
		schedulerEvent("router-abc", "uid-router", "FailedScheduling", base.Add(time.Minute)),
		podEvent("router-abc", "uid-router", "Alpha", base.Add(2*time.Minute)),
		podEvent("unrelated", "uid-other", "Started", base),
	)

	dir := t.TempDir()
	NewKubernetesPodEventDumper(client, "svc in (router)").Dump(t.Context(), dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the selected pod should get an events file")

	content, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	body := string(content)

	assert.Contains(t, body, "FailedScheduling")
	assert.Contains(t, body, "Zulu")
	assert.Contains(t, body, "Alpha")
	assert.NotContains(t, body, "Started", "events of a non-matching pod must not be dumped")

	// Oldest first, so the file reads as the pod's history.
	assert.Less(t, strings.Index(body, "Zulu"), strings.Index(body, "FailedScheduling"),
		"a legacy event older than an events.k8s.io event must come first")
	assert.Less(t, strings.Index(body, "FailedScheduling"), strings.Index(body, "Alpha"),
		"an EventTime-only event must be placed by EventTime, not sorted to the front")
}

func TestPodEventDumperSkipsPodsWithNoEvents(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewClientset(fissionPod("router-abc", "uid-router"))

	dir := t.TempDir()
	NewKubernetesPodEventDumper(client, "svc in (router)").Dump(t.Context(), dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "an empty events file would just be noise in the bundle")
}

func TestPodEventDumperIgnoresNonPodEvents(t *testing.T) {
	t.Parallel()

	pod := fissionPod("router-abc", "uid-router")

	// A Deployment event that happens to carry the pod's UID would be a bug to
	// include; the involvedObject Kind is what decides.
	deployEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "deploy-evt", Namespace: "fission"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Deployment", Name: "router", Namespace: "fission", UID: "uid-router",
		},
		Reason: "ScalingReplicaSet",
	}

	client := k8sfake.NewClientset(pod, deployEvent)

	dir := t.TempDir()
	NewKubernetesPodEventDumper(client, "svc in (router)").Dump(t.Context(), dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "non-Pod events must not be attributed to a pod")
}
