// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// podStream is one watched pod population, named for the timeline.
type podStream struct {
	name      string // timeline label: "control-plane" | "function"
	namespace string // metav1.NamespaceAll for cluster-wide
	selector  string // label selector; "" is every pod in the namespace
}

// watchPodLifecycles streams pod transitions into the recorder for the whole
// load window: the control-plane namespace (which pod restarted when) plus
// every environment-derived pod cluster-wide (pool pods, specialized pods,
// newdeploy function pods — all carry the ENVIRONMENT_NAME label, whatever
// the tenancy layout).
//
// Best-effort by construction: the watcher exists to explain failures, so it
// must never cause one. Every error re-lists and re-watches after a beat; ctx
// cancellation is the only exit.
//
// The returned wait func blocks until both watcher goroutines have exited.
// The caller MUST cancel ctx and then wait before reading the recorder's
// outputs: a straggling rec.event past the event cap is a map write, and a
// concurrent marshal of the dropped map is a fatal throw.
func watchPodLifecycles(ctx context.Context, kube kubernetes.Interface, fissionNS string, rec *upgradeRecorder) (wait func()) {
	streams := []podStream{
		{name: "control-plane", namespace: fissionNS},
		{name: "function", namespace: metav1.NamespaceAll, selector: fv1.ENVIRONMENT_NAME},
	}
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			watchPodStream(ctx, kube, rec, s)
		}()
	}
	return wg.Wait
}

// podState is the per-pod previous view used to derive transition events.
type podState struct {
	ready       bool
	restarts    int32
	terminating bool
}

func watchPodStream(ctx context.Context, kube kubernetes.Interface, rec *upgradeRecorder, s podStream) {
	pods := kube.CoreV1().Pods(s.namespace)
	states := map[string]*podState{}
	for ctx.Err() == nil {
		// (Re-)list: seed known state WITHOUT emitting events — pods that
		// already exist at watcher start are baseline inventory, not
		// transitions. A pod that died during a watch outage therefore
		// vanishes silently (no `deleted` event) — acceptable for a
		// best-effort correlator, and rebuilding the map wholesale is what
		// keeps its stale entry from living forever. The returned
		// resourceVersion anchors the watch so nothing between list and
		// watch is missed.
		list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: s.selector})
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "pod watcher (%s): list: %v\n", s.name, err)
				sleepBeat(ctx)
			}
			continue
		}
		clear(states)
		for i := range list.Items {
			states[podKey(&list.Items[i])] = newPodState(&list.Items[i])
		}

		w, err := pods.Watch(ctx, metav1.ListOptions{LabelSelector: s.selector, ResourceVersion: list.ResourceVersion})
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "pod watcher (%s): watch: %v\n", s.name, err)
				sleepBeat(ctx)
			}
			continue
		}
		consumePodEvents(ctx, rec, s.name, w, states)
		// Unconditional beat before re-listing: an apiserver that closes
		// watches promptly — i.e. one being rolled, the exact window this
		// watcher runs in — would otherwise turn the clean-close path into a
		// tight loop of cluster-wide LISTs, perturbing the measurement this
		// watcher exists to explain.
		sleepBeat(ctx)
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
		// A pod first seen already terminating emits no `terminating` event,
		// by the same rule as the re-list seeding: pre-existing state is
		// inventory, not a transition.
		st := newPodState(pod)
		states[key] = st
		rec.event("pod", key, "added", stream)
		// A pod can be born Ready-true between polls; surface it.
		if st.ready {
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

func newPodState(p *apiv1.Pod) *podState {
	return &podState{ready: isPodReady(p), restarts: podRestarts(p), terminating: p.DeletionTimestamp != nil}
}

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
