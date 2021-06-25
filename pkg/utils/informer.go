package utils

import (
	"fmt"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"
	metricsapi "k8s.io/metrics/pkg/apis/metrics"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

func GetInformerFactoryByExecutor(client *kubernetes.Clientset, executorType v1.ExecutorType) (k8sInformers.SharedInformerFactory, error) {
	executorLabel, err := labels.NewRequirement(v1.EXECUTOR_TYPE, selection.DoubleEquals, []string{string(executorType)})
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector()
	labelSelector.Add(*executorLabel)
	informerFactory := k8sInformers.NewSharedInformerFactoryWithOptions(client, 0,
		k8sInformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = labelSelector.String()
		}))
	return informerFactory, nil
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
			for _, supportedVersion := range supportedMetricsAPIVersions {
				if version.Version == supportedVersion {
					return true
				}
			}
		}
	}
	return false
}

func GetCachedItem(obj apiv1.ObjectReference, informer k8sCache.SharedIndexInformer) (item interface{}, exists bool, err error) {
	store := informer.GetStore()

	item, exists, err = store.Get(obj)
	if err != nil || !exists {
		item, exists, err = store.GetByKey(fmt.Sprintf("%s/%s", obj.Namespace, obj.Name))
	}

	return item, exists, err
}
