/*
Copyright 2022 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package poolmgr

import (
	"strings"
	"testing"
	"time"

	"github.com/dchest/uniuri"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8sCache "k8s.io/client-go/tools/cache"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/manager"
)

func TestPoolPodControllerPodCleanup(t *testing.T) {
	mgr := manager.New()
	t.Cleanup(mgr.Wait)
	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	kubernetesClient := fake.NewClientset()
	fissionClient := fClient.NewClientset()
	factory := make(map[string]genInformer.SharedInformerFactory, 0)
	factory[metav1.NamespaceDefault] = genInformer.NewFilteredSharedInformerFactory(fissionClient, time.Minute*30, metav1.NamespaceDefault, nil)

	executorLabel, err := utils.GetInformerLabelByExecutor(fv1.ExecutorTypePoolmgr)
	if err != nil {
		t.Fatalf("Error creating labels for informer: %v", err)
	}
	gpmInformerFactory := utils.GetInformerFactoryByExecutor(kubernetesClient, executorLabel, time.Minute*30)

	ppc, err := NewPoolPodController(ctx, logger, kubernetesClient, false,
		factory, gpmInformerFactory)
	if err != nil {
		t.Fatalf("Error creating pool pod controller: %v", err)
	}

	executorInstanceID := strings.ToLower(uniuri.NewLen(8))
	metricsClient := metricsclient.NewSimpleClientset()
	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	if err != nil {
		t.Fatalf("Error creating fetcher config: %v", err)
	}
	executor, err := MakeGenericPoolManager(ctx,
		logger,
		fissionClient, kubernetesClient, metricsClient,
		fetcherConfig, executorInstanceID,
		factory, gpmInformerFactory, nil)
	if err != nil {
		t.Fatalf("Error creating generic pool manager: %v", err)
	}
	gpm := executor.(*GenericPoolManager)
	ppc.InjectGpm(gpm)

	go ppc.Run(ctx, ctx.Done(), mgr)

	for _, f := range factory {
		f.Start(ctx.Done())
	}

	for _, informerFactory := range gpmInformerFactory {
		informerFactory.Start(ctx.Done())
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: metav1.NamespaceDefault,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	_, err = kubernetesClient.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Error creating pod: %v", err)
	}

	// Wait for pod to be added to informer
	start := time.Now()
	found := false
	for found == false && time.Since(start) < time.Second*5 {
		t.Log("Waiting for pod to be added to pool")
		pod, err := ppc.podLister[pod.Namespace].Pods(pod.Namespace).Get(pod.Name)
		if err == nil {
			found = true
			t.Logf("Found pod %#v", pod.ObjectMeta)
		}
		time.Sleep(time.Millisecond * 100)
	}
	t.Log("Pod added to pool")

	// Ask the controller to clean up the pod
	key, err := k8sCache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		t.Fatalf("Error creating key: %v", err)
	}
	ppc.spCleanupPodQueue.Add(key)
	start = time.Now()
	for ppc.spCleanupPodQueue.Len() > 0 && time.Since(start) < time.Second*5 {
		time.Sleep(time.Millisecond * 100)
		t.Log("Waiting for pod cleanup to complete")
	}
	t.Log("Cleanup pod queue is empty")

	// Ensure pod is gone
	getPod, err := kubernetesClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err == nil {
		t.Fatalf("Pod %v still exists", getPod.ObjectMeta)
	}
}
