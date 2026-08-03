// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"os"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// watchPodLifecycles streams pod transitions into the recorder for the whole
// load window: the control-plane namespace (which pod restarted when) plus
// every environment-derived pod cluster-wide (pool pods, specialized pods,
// newdeploy function pods — all carry the ENVIRONMENT_NAME label, whatever
// the tenancy layout).
//
// Best-effort by construction: the watcher exists to explain failures, so it
// must never cause one. Every error re-lists and re-watches after a beat; ctx
// cancellation is the only exit.
func watchPodLifecycles(ctx context.Context, kube kubernetes.Interface, fissionNS string, rec *upgradeRecorder) {
	go watchPodStream(ctx, rec, "control-plane", func(ctx context.Context, rv string) (watch.Interface, error) {
		return kube.CoreV1().Pods(fissionNS).Watch(ctx, metav1.ListOptions{ResourceVersion: rv})
	}, func(ctx context.Context) (*apiv1.PodList, error) {
		return kube.CoreV1().Pods(fissionNS).List(ctx, metav1.ListOptions{})
	})
	go watchPodStream(ctx, rec, "function", func(ctx context.Context, rv string) (watch.Interface, error) {
		return kube.CoreV1().Pods(metav1.NamespaceAll).Watch(ctx, metav1.ListOptions{
			LabelSelector: fv1.ENVIRONMENT_NAME, ResourceVersion: rv,
		})
	}, func(ctx context.Context) (*apiv1.PodList, error) {
		return kube.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: fv1.ENVIRONMENT_NAME})
	})
}

// podState is the per-pod previous view used to derive transition events.
type podState struct {
	ready       bool
	restarts    int32
	terminating bool
}

func watchPodStream(
	ctx context.Context,
	rec *upgradeRecorder,
	stream string,
	watchFn func(ctx context.Context, resourceVersion string) (watch.Interface, error),
	listFn func(ctx context.Context) (*apiv1.PodList, error),
) {
	states := map[string]*podState{}
	for ctx.Err() == nil {
		// (Re-)list: seed known state WITHOUT emitting events — pods that
		// already exist at watcher start are baseline inventory, not
		// transitions. The returned resourceVersion anchors the watch so
		// nothing between list and watch is missed.
		list, err := listFn(ctx)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "pod watcher (%s): list: %v\n", stream, err)
				sleepBeat(ctx)
			}
			continue
		}
		for i := range list.Items {
			p := &list.Items[i]
			states[podKey(p)] = &podState{ready: isPodReady(p), restarts: podRestarts(p), terminating: p.DeletionTimestamp != nil}
		}

		w, err := watchFn(ctx, list.ResourceVersion)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "pod watcher (%s): watch: %v\n", stream, err)
				sleepBeat(ctx)
			}
			continue
		}
		consumePodEvents(ctx, rec, stream, w, states)
	}
}

// consumePodEvents drains one watch connection, returning when it closes or
// ctx ends. The caller re-lists and re-watches (handles 410 Gone expiry).
func consumePodEvents(ctx context.Context, rec *upgradeRecorder, stream string, w watch.Interface, states map[string]*podState) {
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.ResultChan():
			if !ok {
				return
			}
			pod, isPod := ev.Object.(*apiv1.Pod)
			if !isPod {
				// Includes watch.Error (e.g. 410 Gone as *metav1.Status):
				// bail out to the re-list path.
				if ev.Type == watch.Error {
					return
				}
				continue
			}
			recordPodTransition(rec, stream, ev.Type, pod, states)
		}
	}
}

// recordPodTransition derives events from the delta between the previous and
// current view of one pod.
func recordPodTransition(rec *upgradeRecorder, stream string, evType watch.EventType, pod *apiv1.Pod, states map[string]*podState) {
	key := podKey(pod)
	switch evType {
	case watch.Deleted:
		delete(states, key)
		rec.event("pod", key, "deleted", stream)
		return
	case watch.Added:
		states[key] = &podState{ready: isPodReady(pod), restarts: podRestarts(pod), terminating: pod.DeletionTimestamp != nil}
		rec.event("pod", key, "added", stream)
		// A pod can be born Ready-true between polls; surface it.
		if states[key].ready {
			rec.event("pod", key, "ready", stream)
		}
		return
	case watch.Modified:
		prev, seen := states[key]
		if !seen {
			prev = &podState{}
			states[key] = prev
		}
		if ready := isPodReady(pod); ready != prev.ready {
			prev.ready = ready
			if ready {
				rec.event("pod", key, "ready", stream)
			} else {
				rec.event("pod", key, "not-ready", stream)
			}
		}
		if r := podRestarts(pod); r > prev.restarts {
			prev.restarts = r
			rec.event("pod", key, "restarted", fmt.Sprintf("%s restarts=%d", stream, r))
		}
		if term := pod.DeletionTimestamp != nil; term && !prev.terminating {
			prev.terminating = true
			rec.event("pod", key, "terminating", stream)
		}
	}
}

func podKey(p *apiv1.Pod) string { return p.Namespace + "/" + p.Name }

func isPodReady(p *apiv1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == apiv1.PodReady {
			return c.Status == apiv1.ConditionTrue
		}
	}
	return false
}

func podRestarts(p *apiv1.Pod) int32 {
	var n int32
	for _, cs := range p.Status.ContainerStatuses {
		n += cs.RestartCount
	}
	return n
}

func sleepBeat(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
}
