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

	controllerClient "github.com/fission/fission/controller/client"
)

type (
	TimerSync struct {
		controller *controllerClient.Client
		timer      *Timer
	}
)

func MakeTimerSync(controller *controllerClient.Client, timer *Timer) *TimerSync {
	ws := &TimerSync{
		controller: controller,
		timer:      timer,
	}
	go ws.syncSvc()
	return ws
}

func (ws *TimerSync) syncSvc() {
	failureCount := 0
	maxFailures := 6
	for {
		triggers, err := ws.controller.TimeTriggerList()
		if err != nil {
			failureCount++
			if failureCount > maxFailures {
				log.Fatalf("Failed to connect to controller: %v", err)
			}
			time.Sleep(10 * time.Second)
			continue
		}
		ws.timer.Sync(triggers)
		time.Sleep(3 * time.Second)
	}
}
