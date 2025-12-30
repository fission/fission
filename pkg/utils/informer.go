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
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
)

func GetInformersForNamespaces(client versioned.Interface, defaultSync time.Duration, kind string) map[string]cache.SharedIndexInformer {
	informers := make(map[string]cache.SharedIndexInformer)
	for _, ns := range DefaultNSResolver().FissionResourceNS {
		factory := genInformer.NewSharedInformerFactoryWithOptions(client, defaultSync, genInformer.WithNamespace(ns)).Core().V1()
		switch kind {
		case fv1.CanaryConfigResource:
			informers[ns] = factory.CanaryConfigs().Informer()
		case fv1.EnvironmentResource:
			informers[ns] = factory.Environments().Informer()
		case fv1.FunctionResource:
			informers[ns] = factory.Functions().Informer()
		case fv1.HttpTriggerResource:
			informers[ns] = factory.HTTPTriggers().Informer()
		case fv1.KubernetesWatchResource:
			informers[ns] = factory.KubernetesWatchTriggers().Informer()
		case fv1.MessageQueueResource:
			informers[ns] = factory.MessageQueueTriggers().Informer()
		case fv1.PackagesResource:
			informers[ns] = factory.Packages().Informer()
		case fv1.TimeTriggerResource:
			informers[ns] = factory.TimeTriggers().Informer()
		default:
			panic("Unknown kind: " + kind)
		}
	}
	return informers
}

func GetK8sInformersForNamespaces(client kubernetes.Interface, defaultSync time.Duration, kind string) map[string]cache.SharedIndexInformer {
	informers := make(map[string]cache.SharedIndexInformer)
	namespaces := DefaultNSResolver()
	for _, ns := range namespaces.FissionNSWithOptions(WithBuilderNs(), WithFunctionNs(), WithDefaultNs()) {
		factory := k8sInformers.NewSharedInformerFactoryWithOptions(client, defaultSync, k8sInformers.WithNamespace(ns))
		switch kind {
		case fv1.Deployments:
			informers[ns] = factory.Apps().V1().Deployments().Informer()
		case fv1.ReplicaSets:
			informers[ns] = factory.Apps().V1().ReplicaSets().Informer()
		case fv1.Pods:
			informers[ns] = factory.Core().V1().Pods().Informer()
		case fv1.Services:
			informers[ns] = factory.Core().V1().Services().Informer()
		case fv1.ConfigMaps:
			informers[ns] = factory.Core().V1().ConfigMaps().Informer()
		case fv1.Secrets:
			informers[ns] = factory.Core().V1().Secrets().Informer()
		default:
			panic("Unknown kind: " + kind)
		}
	}
	return informers
}

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

func GetInformerFactoryByReadyPod(client kubernetes.Interface, namespace string, labelSelector *metav1.LabelSelector) (k8sInformers.SharedInformerFactory, error) {
	informerFactory := k8sInformers.NewSharedInformerFactoryWithOptions(client, 0,
		k8sInformers.WithNamespace(namespace),
		k8sInformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = labels.Set(labelSelector.MatchLabels).AsSelector().String()
			options.FieldSelector = "status.phase=Running"
		}))
	return informerFactory, nil
}

func GetInformerLabelByExecutor(executorType fv1.ExecutorType) (labels.Selector, error) {
	executorLabel, err := labels.NewRequirement(fv1.EXECUTOR_TYPE, selection.DoubleEquals, []string{string(executorType)})
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector()
	labelSelector.Add(*executorLabel)

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
