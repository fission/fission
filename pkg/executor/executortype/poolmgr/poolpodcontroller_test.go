// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"strings"
	"testing"
	"time"

	"github.com/dchest/uniuri"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8sCache "k8s.io/client-go/tools/cache"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestPoolPodControllerPodCleanup(t *testing.T) {
	mgr := &errgroup.Group{}
	t.Cleanup(func() { _ = mgr.Wait() })
	ctx := t.Context()
	logger := loggerfactory.GetLogger()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: metav1.NamespaceDefault,
			// Real poolmgr pods carry this label; the Manager cache filters on it.
			Labels: map[string]string{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	// The pod lives in the kube client (for the Delete) and in the Manager cache
	// client (where spCleanupPodQueueProcessFunc reads it via gpm.crClient).
	kubernetesClient := fake.NewClientset(pod)
	crClient := crfake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(pod).Build()
	fissionClient := fClient.NewClientset()

	ppc := NewPoolPodController(logger, kubernetesClient)

	executorInstanceID := strings.ToLower(uniuri.NewLen(8))
	// TODO: use NewClientset when available in upstream metrics package
	metricsClient := metricsclient.NewSimpleClientset() //nolint:staticcheck
	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	require.NoError(t, err, "Error creating fetcher config")
	executor, err := MakeGenericPoolManager(ctx,
		logger,
		fissionClient, kubernetesClient, metricsClient,
		fetcherConfig, executorInstanceID,
		nil)
	require.NoError(t, err, "Error creating generic pool manager")
	gpm := executor.(*GenericPoolManager)
	gpm.crClient = crClient
	ppc.InjectGpm(gpm)

	go ppc.Run(ctx, ctx.Done(), mgr)

	// Ask the controller to clean up the pod.
	key, err := k8sCache.MetaNamespaceKeyFunc(pod)
	require.NoError(t, err, "Error creating key")
	ppc.spCleanupPodQueue.Add(key)
	start := time.Now()
	for ppc.spCleanupPodQueue.Len() > 0 && time.Since(start) < time.Second*5 {
		time.Sleep(time.Millisecond * 100)
		t.Log("Waiting for pod cleanup to complete")
	}
	t.Log("Cleanup pod queue is empty")

	// Ensure pod is gone.
	getPod, err := kubernetesClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	require.Error(t, err, "Pod %v still exists", getPod.ObjectMeta)
}
