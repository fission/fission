// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"sort"
	"sync"
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
	events        *PodEventIndex
}

func NewKubernetesPodEventDumper(clientset kubernetes.Interface, selector string, events *PodEventIndex) Resource {
	return KubernetesPodEventDumper{
		client:        clientset,
		labelSelector: selector,
		events:        events,
	}
}

// PodEventIndex holds the cluster's pod events, keyed by the UID of the pod
// each was recorded against and sorted oldest first.
//
// It is built at most once and shared by every KubernetesPodEventDumper.
// dump.go registers three of them — components, builders, functions — and runs
// every dumper concurrently, so a list per dumper puts three unfiltered
// full-cluster Event LISTs in flight at the same moment and holds three copies
// of the result. That is the load the single-list design set out to avoid.
type PodEventIndex struct {
	client kubernetes.Interface
	once   sync.Once
	byPod  map[types.UID][]corev1.Event
	err    error
}

func NewPodEventIndex(clientset kubernetes.Interface) *PodEventIndex {
	return &PodEventIndex{client: clientset}
}

// get builds the index on first call and hands the same result to every later
// caller. Sorting happens here rather than at the point of use, so the slices
// handed out are read-only and concurrent dumpers cannot race sorting one.
func (idx *PodEventIndex) get(ctx context.Context) (map[types.UID][]corev1.Event, error) {
	idx.once.Do(func() {
		// involvedObject.kind is a server-side selectable field on core/v1
		// events, so the apiserver drops non-Pod events rather than sending
		// them. The client-side check below stays regardless: the fake
		// clientset used in tests applies label selectors but ignores field
		// selectors, so it is what keeps the tests honest.
		events, err := idx.client.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.kind=Pod",
		})
		if err != nil {
			idx.err = err
			return
		}

		byPod := make(map[types.UID][]corev1.Event)
		for _, e := range events.Items {
			if e.InvolvedObject.Kind != "Pod" {
				continue
			}
			byPod[e.InvolvedObject.UID] = append(byPod[e.InvolvedObject.UID], e)
		}
		for _, evs := range byPod {
			// Oldest first, so each file reads as that pod's history. Events
			// are returned in no guaranteed order. Stable, so events sharing a
			// timestamp keep the order the apiserver returned them in.
			sort.SliceStable(evs, func(i, j int) bool {
				return eventTime(evs[i]).Before(eventTime(evs[j]))
			})
		}
		idx.byPod = byPod
	})
	return idx.byPod, idx.err
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

	// One cluster-wide Event list for the whole dump, indexed by involved-object
	// UID, rather than a request per pod. A busy fission install has many pods,
	// and events are short-lived and numerous; one list keeps the dump from
	// hammering an apiserver that may already be struggling — which is, after
	// all, why someone is collecting a support bundle.
	byPod, err := res.events.get(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting event list: %v", err))
		return
	}

	for _, pod := range pods.Items {
		podEvents := byPod[pod.UID]
		if len(podEvents) == 0 {
			// Nothing recorded, or the events already aged out of etcd. An
			// empty file would just be noise in the bundle.
			continue
		}
		f := getFileName(dumpDir, pod.ObjectMeta)
		writeToFile(f, podEvents)
	}
}
