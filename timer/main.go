/*
Copyright 2017 The Fission Authors.

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
	controllerClient "github.com/fission/fission/controller/client"
	"github.com/fission/fission/publisher"
)

func Start(controllerUrl string, routerUrl string) error {
	controller := controllerClient.MakeClient(controllerUrl)
	poster := publisher.MakeWebhookPublisher(routerUrl)
	MakeTimerSync(controller, MakeTimer(poster))

	return nil
}
