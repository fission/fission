// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

const routerSelector = "svc in (router)"

func routerDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "router", Namespace: "fission", ResourceVersion: "1",
			Labels: map[string]string{"svc": "router"},
		},
	}
}

// dumpOrphans runs the dumper and returns the pod-event files it wrote, the
// notes it left, and the concatenated contents of the pod-event files.
//
// Notes are prefixed with "_" so the bundle's reader sees them first and so
// they never get mistaken for a pod's event history.
func dumpOrphans(t *testing.T, client *k8sfake.Clientset, selectors ...string) (files, notes []string, body string) {
	t.Helper()

	if len(selectors) == 0 {
		selectors = []string{routerSelector}
	}

	dir := t.TempDir()
	NewOrphanedPodEventDumper(client, NewPodEventIndex(client), NewDeploymentIndex(client),
		selectors...).Dump(t.Context(), dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var sb strings.Builder
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "_") {
			notes = append(notes, e.Name())
			continue
		}
		files = append(files, e.Name())
		content, err := os.ReadFile(dir + "/" + e.Name())
		require.NoError(t, err)
		sb.Write(content)
	}
	return files, notes, sb.String()
}

func TestOrphanedPodEventDumperWritesEventsOfDeletedFissionPods(t *testing.T) {
	t.Parallel()

	// The pod is gone — only its events remain. That is the pod being
	// investigated, and the per-pod dumpers iterate live pods so they miss it.
	client := k8sfake.NewClientset(
		routerDeployment(),
		podEvent("router-7d9f8b6c5-abcde", "uid-dead-router", "OOMKilled", time.Now()),
	)

	files, _, body := dumpOrphans(t, client)

	require.Len(t, files, 1)
	assert.Contains(t, files[0], "router-7d9f8b6c5-abcde")
	assert.Contains(t, body, "OOMKilled")
}

func TestOrphanedPodEventDumperIgnoresUnrelatedPodsInTheSameNamespace(t *testing.T) {
	t.Parallel()

	// The privacy guard. Fission functions commonly share a namespace with a
	// user's own workloads, and a bundle goes to a third party — so "any pod
	// event with no live pod" is not an acceptable definition of orphan.
	client := k8sfake.NewClientset(
		routerDeployment(),
		podEvent("someones-database-0", "uid-dead-stranger", "Evicted", time.Now()),
	)

	files, _, _ := dumpOrphans(t, client)

	assert.Empty(t, files,
		"a deleted pod that does not derive from a fission deployment is not ours to collect")
}

func TestOrphanedPodEventDumperSkipsPodsThatStillExist(t *testing.T) {
	t.Parallel()

	// A live pod's events belong to the per-pod dumpers; duplicating them here
	// would double the bundle's event data.
	live := fissionPod("router-7d9f8b6c5-abcde", "uid-live-router")
	live.Namespace = "fission"

	client := k8sfake.NewClientset(
		routerDeployment(), live,
		podEvent("router-7d9f8b6c5-abcde", "uid-live-router", "Started", time.Now()),
	)

	files, _, _ := dumpOrphans(t, client)

	assert.Empty(t, files, "events of a live pod are not orphans")
}

func TestOrphanedPodEventDumperCoversMqtConsumers(t *testing.T) {
	t.Parallel()

	// The mqt consumer Deployment carries no fission label, so it is reachable
	// only through its owner reference — the same signal MqtConsumerDumper uses.
	client := k8sfake.NewClientset(
		mqtDeployment("my-mqt", "fission"),
		podEvent("my-mqt-56c4d9f7b-xyz12", "uid-dead-consumer", "CrashLoopBackOff", time.Now()),
	)

	files, _, body := dumpOrphans(t, client)

	require.Len(t, files, 1)
	assert.Contains(t, body, "CrashLoopBackOff")
}

func TestOrphanedPodEventDumperMatchesPrefixesPerNamespace(t *testing.T) {
	t.Parallel()

	// A name prefix only authorises within the namespace that owns it. The
	// helper builds events in "fission"; the deployment lending the name lives
	// elsewhere, so it must not vouch for them.
	client := k8sfake.NewClientset(
		mqtDeployment("my-mqt", "default"),
		podEvent("my-mqt-56c4d9f7b-xyz12", "uid-dead-elsewhere", "CrashLoopBackOff", time.Now()),
	)

	files, _, _ := dumpOrphans(t, client)

	assert.Empty(t, files, "a same-named deployment in another namespace is a different workload")
}

func TestOrphanedPodEventDumperIgnoresAPodMerelyPrefixedByAFissionDeployment(t *testing.T) {
	t.Parallel()

	// The leak a bare HasPrefix allows. Deployment names here are user-chosen —
	// a function is named by its author, an mqt consumer after its trigger — so
	// fission's "router" would otherwise vouch for a stranger's
	// "router-metrics-<hash>-<rand>" in the same namespace and put their dead
	// pod's events in a bundle bound for a third party.
	client := k8sfake.NewClientset(
		routerDeployment(),
		podEvent("router-metrics-7d9f8b6c5-abcde", "uid-third-party", "Evicted", time.Now()),
	)

	files, _, _ := dumpOrphans(t, client)

	assert.Empty(t, files,
		"a pod whose name merely starts with a fission deployment's name is a different workload")
}

func TestOrphanedPodEventDumperIgnoresEventsWithNoInvolvedObjectUID(t *testing.T) {
	t.Parallel()

	// Events lacking a UID all collapse into one bucket that matches no live
	// pod. Treating that bucket as a single deleted pod would file many
	// unrelated pods' events under whichever one sorted first.
	noUID := podEvent("router-7d9f8b6c5-abcde", "", "OOMKilled", time.Now())
	otherNoUID := podEvent("router-7d9f8b6c5-fffff", "", "Evicted", time.Now())

	client := k8sfake.NewClientset(routerDeployment(), noUID, otherNoUID)

	files, _, _ := dumpOrphans(t, client)

	assert.Empty(t, files, "events with no involvedObject UID cannot be attributed to one pod")
}

func TestOrphanedPodEventDumperSkipsEverythingWhenThePodListIsIncomplete(t *testing.T) {
	t.Parallel()

	// Without the full live-pod set, "no live pod has this UID" stops meaning
	// "this pod is gone" — so emitting would file a running pod under a
	// directory that says it died. Inventing a failure is worse than missing one.
	client := k8sfake.NewClientset(
		routerDeployment(),
		podEvent("router-7d9f8b6c5-abcde", "uid-router", "OOMKilled", time.Now()),
	)

	var podLists int
	client.PrependReactor("list", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
		podLists++
		if podLists == 1 {
			return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "page-2"}}, nil
		}
		return true, nil, apierrors.NewResourceExpired("too old resource version")
	})

	files, notes, _ := dumpOrphans(t, client)

	assert.Empty(t, files, "a live pod must never be reported as deleted")
	assert.Contains(t, notes, "_skipped-incomplete-pod-list.txt",
		"the bundle must record why nothing was collected")
}

func TestOrphanedPodEventDumperHonoursEverySelector(t *testing.T) {
	t.Parallel()

	// dump.go passes three selectors; a dumper honouring only the first would
	// quietly drop builder and function pods from the orphan scan.
	builder := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "builder", Namespace: "fission", ResourceVersion: "1",
			Labels: map[string]string{"owner": "buildermgr"},
		},
	}

	client := k8sfake.NewClientset(
		builder,
		podEvent("builder-7d9f8b6c5-abcde", "uid-dead-builder", "OOMKilled", time.Now()),
	)

	files, _, _ := dumpOrphans(t, client, routerSelector, "owner=buildermgr")

	require.Len(t, files, 1, "a deployment matching the second selector must be honoured too")
}

func TestOrphanedPodEventDumperPagesThePodList(t *testing.T) {
	t.Parallel()

	// The continue token is fed back for pods as well as events; without it a
	// pod on page 2 looks deleted and its events are filed as orphaned.
	live := fissionPod("router-7d9f8b6c5-abcde", "uid-live-router")
	live.Namespace = "fission"

	client := k8sfake.NewClientset(
		routerDeployment(),
		podEvent("router-7d9f8b6c5-abcde", "uid-live-router", "Started", time.Now()),
	)

	client.PrependReactor("list", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.(clienttesting.ListActionImpl).GetListOptions().Continue == "" {
			return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "page-2"}}, nil
		}
		return true, &corev1.PodList{Items: []corev1.Pod{*live}}, nil
	})

	files, _, _ := dumpOrphans(t, client)

	assert.Empty(t, files, "a pod found on the second page is alive, not orphaned")
}
