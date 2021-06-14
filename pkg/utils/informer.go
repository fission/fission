package utils

import (
	v1 "github.com/fission/fission/pkg/apis/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
)

func GetInformerFacoryByExecutor(client *kubernetes.Clientset, executorType v1.ExecutorType) (k8sInformers.SharedInformerFactory, error) {
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
