// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
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
	NewKubernetesPodEventDumper(client, "svc in (router)", NewPodEventIndex(client)).Dump(t.Context(), dir)

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
	NewKubernetesPodEventDumper(client, "svc in (router)", NewPodEventIndex(client)).Dump(t.Context(), dir)

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
	NewKubernetesPodEventDumper(client, "svc in (router)", NewPodEventIndex(client)).Dump(t.Context(), dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "non-Pod events must not be attributed to a pod")
}

func TestPodEventDumperAttributesEventsByUIDNotName(t *testing.T) {
	t.Parallel()

	// Pool and function pods are recreated under recycled names, so a stale
	// event from a dead pod must not be attributed to its replacement. Both
	// events below name "router-abc"; only one carries the live pod's UID.
	pod := fissionPod("router-abc", "uid-live")

	stale := podEvent("router-abc", "uid-dead", "StaleFromDeadPod", time.Now())

	client := k8sfake.NewClientset(
		pod, stale,
		podEvent("router-abc", "uid-live", "LiveEvent", time.Now()),
	)

	dir := t.TempDir()
	NewKubernetesPodEventDumper(client, "svc in (router)", NewPodEventIndex(client)).Dump(t.Context(), dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	content, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)

	assert.Contains(t, string(content), "LiveEvent")
	assert.NotContains(t, string(content), "StaleFromDeadPod",
		"an event naming the pod but carrying another UID belongs to a different pod")
}

func TestPodEventIndexListsOnceAcrossDumpers(t *testing.T) {
	t.Parallel()

	// The three registrations in dump.go run concurrently; without a shared
	// index that is three unfiltered full-cluster Event LISTs at once.
	client := k8sfake.NewClientset(
		fissionPod("router-abc", "uid-router"),
		podEvent("router-abc", "uid-router", "Pulled", time.Now()),
	)

	var lists atomic.Int32
	client.PrependReactor("list", "events", func(clienttesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return false, nil, nil // fall through to the tracker
	})

	// Concurrently, as dump.go runs them — so under -race this also guards the
	// claim that the slices the index hands out are read-only.
	idx := NewPodEventIndex(client)
	wg := &sync.WaitGroup{}
	for range 3 {
		dir := t.TempDir()
		wg.Go(func() {
			NewKubernetesPodEventDumper(client, "svc in (router)", idx).Dump(t.Context(), dir)
		})
	}
	wg.Wait()

	assert.Equal(t, int32(1), lists.Load(), "the event list must be fetched once for the whole dump")
}

func TestPodEventDumperReportsEventListFailure(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewClientset(fissionPod("router-abc", "uid-router"))
	client.PrependReactor("list", "events", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "events"}, "", assert.AnError)
	})

	dir := t.TempDir()
	// Must degrade to "no event files" rather than aborting the bundle.
	require.NotPanics(t, func() {
		NewKubernetesPodEventDumper(client, "svc in (router)", NewPodEventIndex(client)).Dump(t.Context(), dir)
	})

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestPodEventIndexFiltersServerSide(t *testing.T) {
	t.Parallel()

	// The fake clientset applies label selectors but ignores field selectors,
	// so a behavioural assertion cannot see this. Inspect the action instead.
	client := k8sfake.NewClientset(
		fissionPod("router-abc", "uid-router"),
		podEvent("router-abc", "uid-router", "Pulled", time.Now()),
	)

	var fields string
	client.PrependReactor("list", "events", func(a clienttesting.Action) (bool, runtime.Object, error) {
		fields = a.(clienttesting.ListAction).GetListRestrictions().Fields.String()
		return false, nil, nil // fall through to the tracker
	})

	dir := t.TempDir()
	NewKubernetesPodEventDumper(client, "svc in (router)", NewPodEventIndex(client)).Dump(t.Context(), dir)

	assert.Equal(t, "involvedObject.kind=Pod", fields,
		"non-Pod events must be dropped by the apiserver, not shipped and discarded client-side")
}
