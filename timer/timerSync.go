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
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/tpr"
)

type (
	TimerSync struct {
		fissionClient *tpr.FissionClient
		timer         *Timer
	}
)

func MakeTimerSync(fissionClient *tpr.FissionClient, timer *Timer) *TimerSync {
	ws := &TimerSync{
		fissionClient: fissionClient,
		timer:         timer,
	}
	go ws.syncSvc()
	return ws
}

func (ws *TimerSync) syncSvc() {
	for {
		triggers, err := ws.fissionClient.Timetriggers(metav1.NamespaceAll).List(metav1.ListOptions{})
		if err != nil {
			log.Fatalf("Failed get time trigger list: %v", err)
		}
		ws.timer.Sync(triggers.Items)

		// TODO switch to watches
		time.Sleep(3 * time.Second)
	}
}
