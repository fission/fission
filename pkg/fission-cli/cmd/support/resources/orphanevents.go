// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"path/filepath"
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
	byPod, err := res.events.get(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting event list: %v", err))
		return
	}
	if len(byPod) == 0 {
		return
	}

	prefixes := res.fissionPodPrefixes(ctx)
	if len(prefixes) == 0 {
		return
	}

	live, err := res.livePodUIDs(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting pod list: %v", err))
		return
	}

	for uid, events := range byPod {
		if _, alive := live[uid]; alive || len(events) == 0 {
			continue
		}

		ref := events[0].InvolvedObject
		if !derivesFromFissionDeployment(prefixes, ref.Namespace, ref.Name) {
			continue
		}

		f := filepath.Clean(fmt.Sprintf("%v/%v_%v_%v.txt", dumpDir, ref.Namespace, ref.Name, uid))
		writeToFile(f, events)
	}
}

// fissionPodPrefixes maps a namespace to the pod-name prefixes of the fission
// Deployments in it — the components, builders and functions the bundle already
// collects, plus the mqt consumers found by owner reference.
func (res OrphanedPodEventDumper) fissionPodPrefixes(ctx context.Context) map[string][]string {
	deployments, err := res.deployments.get(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting deployment list: %v", err))
		return nil
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
		if !isFissionDeployment(d.Labels, d.ObjectMeta, parsed) {
			continue
		}
		prefixes[d.Namespace] = append(prefixes[d.Namespace], d.Name+"-")
	}
	return prefixes
}

func isFissionDeployment(lbls map[string]string, meta metav1.ObjectMeta, selectors []labels.Selector) bool {
	if ownedByMessageQueueTrigger(meta) {
		return true
	}
	set := labels.Set(lbls)
	for _, sel := range selectors {
		if sel.Matches(set) {
			return true
		}
	}
	return false
}

func derivesFromFissionDeployment(prefixes map[string][]string, namespace, name string) bool {
	for _, p := range prefixes[namespace] {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (res OrphanedPodEventDumper) livePodUIDs(ctx context.Context) (map[types.UID]struct{}, error) {
	pods, err := listAllPages("pod", func(cont string) ([]corev1.Pod, string, error) {
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
		return nil, err
	}

	live := make(map[types.UID]struct{}, len(pods))
	for _, p := range pods {
		live[p.UID] = struct{}{}
	}
	return live, nil
}
