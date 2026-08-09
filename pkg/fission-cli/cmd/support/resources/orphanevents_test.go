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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
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

// dumpOrphans runs the dumper and returns the concatenated contents written.
func dumpOrphans(t *testing.T, client *k8sfake.Clientset) (files []string, body string) {
	t.Helper()

	dir := t.TempDir()
	NewOrphanedPodEventDumper(client, NewPodEventIndex(client), NewDeploymentIndex(client),
		routerSelector).Dump(t.Context(), dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var sb strings.Builder
	for _, e := range entries {
		files = append(files, e.Name())
		content, err := os.ReadFile(dir + "/" + e.Name())
		require.NoError(t, err)
		sb.Write(content)
	}
	return files, sb.String()
}

func TestOrphanedPodEventDumperWritesEventsOfDeletedFissionPods(t *testing.T) {
	t.Parallel()

	// The pod is gone — only its events remain. That is the pod being
	// investigated, and the per-pod dumpers iterate live pods so they miss it.
	client := k8sfake.NewClientset(
		routerDeployment(),
		podEvent("router-7d9f8b6c5-abcde", "uid-dead-router", "OOMKilled", time.Now()),
	)

	files, body := dumpOrphans(t, client)

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

	files, _ := dumpOrphans(t, client)

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

	files, _ := dumpOrphans(t, client)

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

	files, body := dumpOrphans(t, client)

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

	files, _ := dumpOrphans(t, client)

	assert.Empty(t, files, "a same-named deployment in another namespace is a different workload")
}
