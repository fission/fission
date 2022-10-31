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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

type (
	WatchSync struct {
		logger      *zap.Logger
		client      versioned.Interface
		kubeWatcher *KubeWatcher
	}
)

func MakeWatchSync(ctx context.Context, logger *zap.Logger, client versioned.Interface, kubeWatcher *KubeWatcher) *WatchSync {
	ws := &WatchSync{
		logger:      logger.Named("watch_sync"),
		client:      client,
		kubeWatcher: kubeWatcher,
	}

	for _, namespace := range utils.GetNamespaces() {
		go ws.syncSvc(ctx, namespace)
	}
	return ws
}

func (ws *WatchSync) syncSvc(ctx context.Context, namespace string) {
	// TODO watch instead of polling
	for {
		watches, err := ws.client.CoreV1().KubernetesWatchTriggers(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			ws.logger.Fatal("failed to get Kubernetes watch trigger list", zap.Error(err), zap.String("namespace", namespace))
		}

		err = ws.kubeWatcher.Sync(watches.Items)
		if err != nil {
			ws.logger.Fatal("failed to sync watches", zap.Error(err))
		}

		time.Sleep(3 * time.Second)
	}
}
