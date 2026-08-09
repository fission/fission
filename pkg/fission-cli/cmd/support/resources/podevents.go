// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/fission-cli/console"
)

// KubernetesPodEventDumper dumps the Events kubernetes recorded against each
// pod matching labelSelector.
//
// The pod spec dumpers already write the whole Pod object, status included, so
// the diagnostic left missing was the event stream — FailedScheduling,
// ImagePullBackOff, Unhealthy, OOMKilled and friends. Those live on Event
// objects, not on the pod, and they are what explains a pod that never became
// ready.
type KubernetesPodEventDumper struct {
	client        kubernetes.Interface
	labelSelector string
}

func NewKubernetesPodEventDumper(clientset kubernetes.Interface, selector string) Resource {
	return KubernetesPodEventDumper{
		client:        clientset,
		labelSelector: selector,
	}
}

// eventTime is the effective time of an event, whichever API recorded it.
// Sorting on LastTimestamp alone is wrong: an event written through
// events.k8s.io/v1 carries only EventTime, so it reads back through the core/v1
// endpoint with a zero LastTimestamp and sorts ahead of everything else. That
// hits FailedScheduling in particular — kube-scheduler has used the newer
// recorder since 1.19, while kubelet still uses the legacy one — which is the
// event most worth placing correctly in a pod's history.
func eventTime(e corev1.Event) time.Time {
	if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		return e.Series.LastObservedTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

func (res KubernetesPodEventDumper) Dump(ctx context.Context, dumpDir string) {
	pods, err := res.client.CoreV1().
		Pods(metav1.NamespaceAll).
		List(ctx, metav1.ListOptions{LabelSelector: res.labelSelector})
	if err != nil {
		console.Error(fmt.Sprintf("Error getting pod list with selector %v: %v", res.labelSelector, err))
		return
	}
	if len(pods.Items) == 0 {
		return
	}

	// One cluster-wide Event list indexed by involved-object UID, rather than a
	// per-pod field-selector query. A busy fission install has many pods, and
	// events are short-lived and numerous; one list keeps the dump from issuing
	// a request per pod against an apiserver that may already be struggling —
	// which is, after all, why someone is collecting a support bundle.
	events, err := res.client.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		console.Error(fmt.Sprintf("Error getting event list: %v", err))
		return
	}

	byPod := make(map[types.UID][]corev1.Event)
	for _, e := range events.Items {
		if e.InvolvedObject.Kind != "Pod" {
			continue
		}
		byPod[e.InvolvedObject.UID] = append(byPod[e.InvolvedObject.UID], e)
	}

	for _, pod := range pods.Items {
		podEvents := byPod[pod.UID]
		if len(podEvents) == 0 {
			// Nothing recorded, or the events already aged out of etcd. An
			// empty file would just be noise in the bundle.
			continue
		}

		// Oldest first, so the file reads as the pod's history. Events are
		// returned in no guaranteed order. Stable, so events sharing a
		// timestamp keep the order the apiserver returned them in.
		sort.SliceStable(podEvents, func(i, j int) bool {
			return eventTime(podEvents[i]).Before(eventTime(podEvents[j]))
		})

		f := getFileName(dumpDir, pod.ObjectMeta)
		writeToFile(f, podEvents)
	}
}
