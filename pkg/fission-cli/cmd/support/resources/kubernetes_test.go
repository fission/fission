// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func loggingPod() *corev1.Pod {
	p := fissionPod("router-abc", "uid-router")
	p.Spec.Containers = []corev1.Container{{Name: "router"}, {Name: "sidecar"}}
	p.Spec.InitContainers = []corev1.Container{{Name: "fetcher"}}
	return p
}

func TestPodLogDumperWritesAFilePerContainerIncludingInitContainers(t *testing.T) {
	t.Parallel()

	// Init container logs are what explain a pod that never reached Running,
	// which is the pod a support bundle is usually collected for.
	client := k8sfake.NewClientset(loggingPod())

	dir := t.TempDir()
	NewKubernetesPodLogDumper(client, "svc in (router)").Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 3)

	joined := strings.Join(files, " ")
	assert.Contains(t, joined, "router")
	assert.Contains(t, joined, "sidecar")
	assert.Contains(t, joined, "fetcher", "init containers must be collected too")
}

func TestPodLogDumperKeepsGoingWhenOneContainerFails(t *testing.T) {
	t.Parallel()

	// The usual failure is a container still PodInitializing. Regular
	// containers stream before init containers, so aborting the pod on the
	// first failure would leave the stuck pod with no logs at all — which reads
	// exactly like a pod that logged nothing.
	client := k8sfake.NewClientset(loggingPod())
	client.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, assert.AnError
	})

	dir := t.TempDir()
	require.NotPanics(t, func() {
		NewKubernetesPodLogDumper(client, "svc in (router)").Dump(t.Context(), dir)
	})
}
