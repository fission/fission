package poolmgr

import (
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

func (gp *GenericPool) newPodInformer() cache.SharedIndexInformer {
	optionsModifier := func(options *metav1.ListOptions) {
		options.LabelSelector = labels.Set(
			gp.deployment.Spec.Selector.MatchLabels).AsSelector().String()
		options.FieldSelector = "status.phase=Running"
	}
	return informers.NewFilteredPodInformer(gp.kubernetesClient, gp.namespace, 0, nil, optionsModifier)
}

func (gp *GenericPool) startReadyPodController() {
	// create the pod watcher to filter by labels
	// Filtering pod by phase=Running. In some cases the pod can be in
	// different state than Running, for example Kubernetes sets a
	// pod to Termination while k8s waits for the grace period of
	// the pod, even if all the containers are in Ready state.
	gp.readyPodQueue = workqueue.NewDelayingQueue()
	gp.readyPodInformer = gp.newPodInformer()
	gp.readyPodInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				gp.readyPodQueue.AddAfter(key, 100*time.Millisecond)
				gp.logger.Debug("add func called", zap.String("key", key))
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				gp.readyPodQueue.Done(key)
				gp.logger.Debug("delete func called", zap.String("key", key))
			}
		},
	})
	go gp.readyPodInformer.Run(gp.stopReadyPodControllerCh)
	gp.logger.Info("readyPod controller started", zap.String("env", gp.env.ObjectMeta.Name), zap.String("envID", string(gp.env.ObjectMeta.UID)))
}
