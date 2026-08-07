// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"sort"

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
		// returned in no guaranteed order.
		sort.Slice(podEvents, func(i, j int) bool {
			return podEvents[i].LastTimestamp.Before(&podEvents[j].LastTimestamp)
		})

		f := getFileName(dumpDir, pod.ObjectMeta)
		writeToFile(f, podEvents)
	}
}
