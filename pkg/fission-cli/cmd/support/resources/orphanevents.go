// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/fission-cli/console"
)

// OrphanedPodEventDumper writes the events of fission pods that no longer
// exist.
//
// KubernetesPodEventDumper iterates pods first, so an event only reaches the
// bundle if its pod is still listed. The pod that was OOMKilled, evicted, or
// replaced by a rolling update is exactly the one being investigated, and its
// events were the one thing that survived it.
//
// The naive version of this — "every Pod event whose UID matches no live pod" —
// would put unrelated teams' deleted pods into a bundle sent to a third party
// whenever fission functions share a namespace with other workloads. So an
// orphan counts only when its name derives from a fission-owned Deployment:
// every pod a Deployment owns is named <deployment>-<rs hash>-<random>, and
// that is checkable without the pod existing.
//
// Two classes are knowingly out of scope, both because they are not
// Deployment-owned: pods from the chart's Helm hook Jobs (pre-upgrade checks,
// analytics, tenant migration), and anything not created by fission at all.
// Fission itself creates no bare Pods or StatefulSets at runtime, so those two
// are the whole exception list.
type OrphanedPodEventDumper struct {
	client      kubernetes.Interface
	events      *PodEventIndex
	deployments *DeploymentIndex
	selectors   []string
}

func NewOrphanedPodEventDumper(clientset kubernetes.Interface, events *PodEventIndex,
	deployments *DeploymentIndex, selectors ...string) Resource {
	return OrphanedPodEventDumper{
		client:      clientset,
		events:      events,
		deployments: deployments,
		selectors:   selectors,
	}
}

func (res OrphanedPodEventDumper) Dump(ctx context.Context, dumpDir string) {
	byPod, eventsPartial, err := res.events.get(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting event list: %v", err))
		writeNote(dumpDir, "_event-list-failed.txt", "no orphaned events dumped: %v", err)
		return
	}

	prefixes, err := res.fissionPodPrefixes(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting deployment list: %v", err))
		writeNote(dumpDir, "_deployment-list-failed.txt",
			"no orphaned events dumped: the deployments that identify fission pods could not be listed: %v", err)
		return
	}

	live, livePartial, err := res.livePodUIDs(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting pod list: %v", err))
		writeNote(dumpDir, "_pod-list-failed.txt", "no orphaned events dumped: %v", err)
		return
	}
	if livePartial {
		// Without the full set of live pods, "no live pod has this UID" stops
		// meaning "this pod is gone". Emitting anyway would file a running pod
		// under a directory that says it died — inventing a failure rather than
		// missing one, which is the worse mistake for a diagnostic bundle.
		console.Error("Pod list incomplete; skipping orphaned pod events to avoid reporting live pods as deleted")
		writeNote(dumpDir, "_skipped-incomplete-pod-list.txt",
			"orphaned events were not collected: the pod list did not complete, so a live pod could not be "+
				"told apart from a deleted one")
		return
	}

	written := 0
	for uid, events := range byPod {
		// An event may carry no involvedObject UID at all. Those all collapse
		// into one bucket that matches no live pod, so treating it as a single
		// deleted pod would file many unrelated pods' events under whichever
		// one happened to sort first.
		if uid == "" || len(events) == 0 {
			continue
		}
		if _, alive := live[uid]; alive {
			continue
		}

		ref := events[0].InvolvedObject
		if !derivesFromFissionDeployment(prefixes, ref.Namespace, ref.Name) {
			continue
		}

		f := filepath.Clean(fmt.Sprintf("%v/%v_%v_%v.txt", dumpDir, ref.Namespace, ref.Name, uid))
		writeToFile(f, events)
		written++
	}

	if written == 0 || eventsPartial {
		writeNote(dumpDir, "_status.txt",
			"scanned %v pods with events, %v live; wrote %v orphaned pod event files (event list complete: %v)",
			len(byPod), len(live), written, !eventsPartial)
	}
}

// fissionPodPrefixes maps a namespace to the pod-name prefixes of the fission
// Deployments in it — the components, builders and functions the bundle already
// collects, plus the mqt consumers found by owner reference.
func (res OrphanedPodEventDumper) fissionPodPrefixes(ctx context.Context) (map[string][]string, error) {
	deployments, _, err := res.deployments.get(ctx)
	if err != nil {
		return nil, err
	}

	parsed := make([]labels.Selector, 0, len(res.selectors))
	for _, s := range res.selectors {
		sel, err := labels.Parse(s)
		if err != nil {
			console.Error(fmt.Sprintf("Error parsing selector %v: %v", s, err))
			continue
		}
		parsed = append(parsed, sel)
	}

	prefixes := make(map[string][]string)
	for _, d := range deployments {
		if !isFissionDeployment(d.ObjectMeta, parsed) {
			continue
		}
		prefixes[d.Namespace] = append(prefixes[d.Namespace], d.Name+"-")
	}
	return prefixes, nil
}

func isFissionDeployment(meta metav1.ObjectMeta, selectors []labels.Selector) bool {
	if ownedByMessageQueueTrigger(meta) {
		return true
	}
	set := labels.Set(meta.Labels)
	for _, sel := range selectors {
		if sel.Matches(set) {
			return true
		}
	}
	return false
}

// replicaSetPodSuffix is what a Deployment-owned pod's name looks like after its
// Deployment's name: the ReplicaSet's pod-template hash, then a random suffix.
var replicaSetPodSuffix = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]{5}$`)

// derivesFromFissionDeployment reports whether a pod that no longer exists
// belonged to one of the given Deployments.
//
// A bare prefix test is not enough, and getting this wrong leaks. Deployment
// names here are user-chosen — a function is named by its author, and an mqt
// consumer is named after the trigger — so "starts with <name>-" would let a
// fission function called api vouch for a stranger's api-gateway-<hash>-<rand>
// in the same namespace, and put their pod's events in a bundle bound for a
// third party. Requiring the ReplicaSet suffix shape rejects that without ever
// rejecting a real one, because every Deployment-owned pod is named this way.
func derivesFromFissionDeployment(prefixes map[string][]string, namespace, name string) bool {
	for _, p := range prefixes[namespace] {
		rest, ok := strings.CutPrefix(name, p)
		if ok && replicaSetPodSuffix.MatchString(rest) {
			return true
		}
	}
	return false
}

func (res OrphanedPodEventDumper) livePodUIDs(ctx context.Context) (map[types.UID]struct{}, bool, error) {
	pods, partial, err := listAllPages("pod", func(cont string) ([]corev1.Pod, string, error) {
		list, err := res.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			Limit:    listPageSize,
			Continue: cont,
		})
		if err != nil {
			return nil, "", err
		}
		return list.Items, list.Continue, nil
	})
	if err != nil {
		return nil, false, err
	}

	live := make(map[types.UID]struct{}, len(pods))
	for _, p := range pods {
		live[p.UID] = struct{}{}
	}
	return live, partial, nil
}
