package poolmgr

import (
	"time"

	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

func (gp *GenericPool) startReadyPodController() {
	// create the pod watcher to filter by labels
	// Filtering pod by phase=Running. In some cases the pod can be in
	// different sate than Running, for example Kubernetes sets a
	// pod to Termination while k8s waits for the grace period of
	// the pod, even if all the containers are in Ready state.
	optionsModifier := func(options *metav1.ListOptions) {
		options.LabelSelector = labels.Set(
			gp.deployment.Spec.Selector.MatchLabels).AsSelector().String()
		options.FieldSelector = "status.phase=Running"
	}
	readyPodWatcher := cache.NewFilteredListWatchFromClient(gp.kubernetesClient.CoreV1().RESTClient(), "pods", gp.namespace, optionsModifier)

	gp.readyPodQueue = workqueue.NewDelayingQueue()
	gp.readyPodIndexer, gp.readyPodController = cache.NewIndexerInformer(readyPodWatcher, &apiv1.Pod{}, 0, cache.ResourceEventHandlerFuncs{
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
	}, cache.Indexers{})
	go gp.readyPodController.Run(gp.stopReadyPodControllerCh)
	gp.logger.Info("readyPod controller started", zap.String("env", gp.env.ObjectMeta.Name), zap.String("envID", string(gp.env.ObjectMeta.UID)))
}
