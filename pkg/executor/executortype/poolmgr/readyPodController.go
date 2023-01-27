package poolmgr

import (
	"time"

	"go.uber.org/zap"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/fission/fission/pkg/utils"
)

func (gp *GenericPool) readyPodEventHandlers() k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := k8sCache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				gp.readyPodQueue.AddAfter(key, 100*time.Millisecond)
				gp.logger.Debug("add func called", zap.String("key", key))
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := k8sCache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				gp.readyPodQueue.Done(key)
				gp.logger.Debug("delete func called", zap.String("key", key))
			}
		},
	}
}

func (gp *GenericPool) setupReadyPodController() error {
	gp.readyPodQueue = workqueue.NewDelayingQueue()
	informerFactory, err := utils.GetInformerFactoryByReadyPod(gp.kubernetesClient, gp.fnNamespace, gp.deployment.Spec.Selector)
	if err != nil {
		return err
	}
	podInformer := informerFactory.Core().V1().Pods()
	gp.readyPodLister = podInformer.Lister()
	gp.readyPodListerSynced = podInformer.Informer().HasSynced
	podInformer.Informer().AddEventHandler(gp.readyPodEventHandlers())
	go podInformer.Informer().Run(gp.stopReadyPodControllerCh)
	gp.logger.Info("readyPod controller started", zap.String("env", gp.env.ObjectMeta.Name), zap.String("envID", string(gp.env.ObjectMeta.UID)))
	return nil
}
