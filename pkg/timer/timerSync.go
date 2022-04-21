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

package timer

import (
	"context"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

type (
	TimerSync struct {
		logger        *zap.Logger
		fissionClient versioned.Interface
		timer         *Timer
	}
)

func MakeTimerSync(ctx context.Context, logger *zap.Logger, fissionClient versioned.Interface, timer *Timer) *TimerSync {
	ws := &TimerSync{
		logger:        logger.Named("timer_sync"),
		fissionClient: fissionClient,
		timer:         timer,
	}
	go ws.syncSvc(ctx)
	return ws
}

func (ws *TimerSync) syncSvc(ctx context.Context) {
	for {
		triggers, err := ws.fissionClient.CoreV1().TimeTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			if utils.IsNetworkError(err) {
				ws.logger.Info("encountered a network error - will retry", zap.Error(err))
				time.Sleep(5 * time.Second)
				continue
			}
			ws.logger.Fatal("failed to get time trigger list", zap.Error(err))
		}
		ws.timer.Sync(triggers.Items) //nolint: errCheck

		// TODO switch to watches
		time.Sleep(3 * time.Second)
	}
}
