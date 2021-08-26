package utils

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	metricsapi "k8s.io/metrics/pkg/apis/metrics"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

func GetInformerFactoryByExecutor(client *kubernetes.Clientset, executorType v1.ExecutorType, defaultResync time.Duration) (k8sInformers.SharedInformerFactory, error) {
	executorLabel, err := labels.NewRequirement(v1.EXECUTOR_TYPE, selection.DoubleEquals, []string{string(executorType)})
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector()
	labelSelector.Add(*executorLabel)
	informerFactory := k8sInformers.NewSharedInformerFactoryWithOptions(client, defaultResync,
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
