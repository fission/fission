package utils

import (
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	metricsapi "k8s.io/metrics/pkg/apis/metrics"

	v1 "github.com/fission/fission/pkg/apis/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
)

const additionalNamespaces string = "FISSION_RESOURCE_NAMESPACES"

func GetNamespaces() []string {
	envValue := os.Getenv(additionalNamespaces)
	if len(envValue) == 0 {
		return []string{
			metav1.NamespaceDefault,
		}
	}

	informerNS := make([]string, 0)
	lstNamespaces := strings.Split(envValue, ",")
	for _, namespace := range lstNamespaces {
		//check to handle string with additional comma at the end of string. eg- ns1,ns2,
		if namespace != "" {
			informerNS = append(informerNS, namespace)
		}
	}
	return informerNS
}

func GetInformersForNamespaces(client versioned.Interface, defaultSync time.Duration, kind string) map[string]cache.SharedIndexInformer {
	informers := make(map[string]cache.SharedIndexInformer)
	for _, ns := range GetNamespaces() {
		factory := genInformer.NewFilteredSharedInformerFactory(client, defaultSync, ns, nil).Core().V1()
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

func GetInformerFactoryByReadyPod(client kubernetes.Interface, namespace string, labelSelector *metav1.LabelSelector) (k8sInformers.SharedInformerFactory, error) {
	informerFactory := k8sInformers.NewSharedInformerFactoryWithOptions(client, 0,
		k8sInformers.WithNamespace(namespace),
		k8sInformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = labels.Set(labelSelector.MatchLabels).AsSelector().String()
			options.FieldSelector = "status.phase=Running"
		}))
	return informerFactory, nil
}

func GetInformerFactoryByExecutor(client kubernetes.Interface, executorType v1.ExecutorType, defaultResync time.Duration) (k8sInformers.SharedInformerFactory, error) {
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
