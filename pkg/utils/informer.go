// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"fmt"
	"slices"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	metricsapi "k8s.io/metrics/pkg/apis/metrics"
)

func GetInformerEventChecker(ctx context.Context, client kubernetes.Interface, reason string) map[string]cache.SharedInformer {
	informers := make(map[string]cache.SharedInformer)
	// Cluster mode: function pods (and their websocket-connection events) live in
	// any namespace, so watch Events cluster-wide via a single informer keyed ""
	// (the executor holds the matching cluster-wide events read in cluster mode).
	// Other modes build one per-namespace informer over the Fission namespaces.
	watchNamespaces := listNamespaces(DefaultNSResolver().FissionNSWithOptions(WithBuilderNs(), WithFunctionNs(), WithDefaultNs()))
	if ClusterTenancyEnabled() {
		watchNamespaces = []string{metav1.NamespaceAll}
	}
	for _, ns := range watchNamespaces {
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
