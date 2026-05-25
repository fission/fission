// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatcher

import (
	"context"
	"time"

	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

type (
	WatchSync struct {
		logger              logr.Logger
		client              versioned.Interface
		kubeWatcher         *KubeWatcher
		kubeWatcherInformer map[string]k8sCache.SharedIndexInformer
	}
)

func MakeWatchSync(ctx context.Context, logger logr.Logger, client versioned.Interface, kubeWatcher *KubeWatcher) (*WatchSync, error) {
	ws := &WatchSync{
		logger:      logger.WithName("watch_sync"),
		client:      client,
		kubeWatcher: kubeWatcher,
	}
	ws.kubeWatcherInformer = utils.GetInformersForNamespaces(client, time.Minute*30, fv1.KubernetesWatchResource)
	err := ws.KubeWatcherEventHandlers(ctx)
	if err != nil {
		return nil, err
	}
	return ws, nil
}

func (ws *WatchSync) Run(ctx context.Context, mgr manager.Interface) {
	mgr.AddInformers(ctx, ws.kubeWatcherInformer)
}

func (ws *WatchSync) KubeWatcherEventHandlers(ctx context.Context) error {
	for _, informer := range ws.kubeWatcherInformer {
		_, err := informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				objKubeWatcher := obj.(*fv1.KubernetesWatchTrigger)
				ws.kubeWatcher.addWatch(ctx, objKubeWatcher) //nolint: errCheck
			},
			DeleteFunc: func(obj any) {
				objKubeWatcher := obj.(*fv1.KubernetesWatchTrigger)
				ws.kubeWatcher.removeWatch(objKubeWatcher) //nolint: errCheck
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}
