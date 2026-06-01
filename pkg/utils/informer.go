// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"fmt"
	"slices"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/watch"
	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	metricsapi "k8s.io/metrics/pkg/apis/metrics"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func GetInformerEventChecker(ctx context.Context, client kubernetes.Interface, reason string) map[string]cache.SharedInformer {
	informers := make(map[string]cache.SharedInformer)
	namespaces := DefaultNSResolver()
	for _, ns := range namespaces.FissionNSWithOptions(WithBuilderNs(), WithFunctionNs(), WithDefaultNs()) {
		informers[ns] = cache.NewSharedInformer(
			&cache.ListWatch{
				ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
					options.FieldSelector = fmt.Sprintf("involvedObject.kind=Pod,type=Normal,reason=%s", reason)
					return client.CoreV1().Events(ns).List(ctx, options)
				},
				WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
					options.FieldSelector = fmt.Sprintf("involvedObject.kind=Pod,type=Normal,reason=%s", reason)
					return client.CoreV1().Events(ns).Watch(ctx, options)
				},
			},
			&apiv1.Event{},
			0,
		)
	}
	return informers
}

func GetInformerFactoryByExecutor(client kubernetes.Interface, labels labels.Selector, defaultResync time.Duration) map[string]k8sInformers.SharedInformerFactory {
	informerFactory := make(map[string]k8sInformers.SharedInformerFactory)

	namespaces := DefaultNSResolver()
	for _, ns := range namespaces.FissionNSWithOptions(WithBuilderNs(), WithFunctionNs(), WithDefaultNs()) {
		factory := k8sInformers.NewSharedInformerFactoryWithOptions(client, defaultResync,
			k8sInformers.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.LabelSelector = labels.String()
			}),
			k8sInformers.WithNamespace(ns))

		informerFactory[ns] = factory
	}
	return informerFactory
}

func GetInformerLabelByExecutor(executorType fv1.ExecutorType) (labels.Selector, error) {
	executorLabel, err := labels.NewRequirement(fv1.EXECUTOR_TYPE, selection.DoubleEquals, []string{string(executorType)})
	if err != nil {
		return nil, err
	}
	// labels.Selector.Add returns a new selector and does not mutate the
	// receiver, so the result must be assigned back. Dropping it left an empty
	// selector that matched every pod cluster-wide, so the executor informers
	// cached all pods and OOMed at scale (issue #2775).
	labelSelector := labels.NewSelector().Add(*executorLabel)

	return labelSelector, nil
}

func SupportedMetricsAPIVersionAvailable(discoveredAPIGroups *metav1.APIGroupList) bool {
	var supportedMetricsAPIVersions = []string{
		"v1beta1",
	}
	for _, discoveredAPIGroup := range discoveredAPIGroups.Groups {
		if discoveredAPIGroup.Name != metricsapi.GroupName {
			continue
		}
		for _, version := range discoveredAPIGroup.Versions {
			if slices.Contains(supportedMetricsAPIVersions, version.Version) {
				return true
			}
		}
	}
	return false
}
