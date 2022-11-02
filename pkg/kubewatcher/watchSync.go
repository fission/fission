/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kubewatcher

import (
	"context"
	"time"

	"go.uber.org/zap"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

type (
	WatchSync struct {
		logger              *zap.Logger
		client              versioned.Interface
		kubeWatcher         *KubeWatcher
		kubeWatcherInformer map[string]k8sCache.SharedIndexInformer
	}
)

func MakeWatchSync(ctx context.Context, logger *zap.Logger, client versioned.Interface, kubeWatcher *KubeWatcher) *WatchSync {
	ws := &WatchSync{
		logger:      logger.Named("watch_sync"),
		client:      client,
		kubeWatcher: kubeWatcher,
	}
	ws.kubeWatcherInformer = utils.GetInformersForNamespaces(client, time.Minute*30, fv1.KubernetesWatchResource)
	ws.KubeWatcherEventHandlers(ctx)
	return ws
}

func (ws *WatchSync) Run(ctx context.Context) {
	for _, informer := range ws.kubeWatcherInformer {
		go informer.Run(ctx.Done())
	}
}

func (ws *WatchSync) KubeWatcherEventHandlers(ctx context.Context) {
	for _, informer := range ws.kubeWatcherInformer {
		informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				objKubeWatcher := obj.(*fv1.KubernetesWatchTrigger)
				ws.kubeWatcher.addWatch(ctx, objKubeWatcher) //nolint: errCheck
			},
			DeleteFunc: func(obj interface{}) {
				objKubeWatcher := obj.(*fv1.KubernetesWatchTrigger)
				ws.kubeWatcher.removeWatch(objKubeWatcher) //nolint: errCheck
			},
		})
	}
}
