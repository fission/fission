/*
Copyright 2019 The Fission Authors.

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

package fake

import (
	"github.com/fission/fission/pkg/controller/client/rest"
	v1 "github.com/fission/fission/pkg/controller/client/v1"
)

type (
	FakeV1 struct{}
)

func MakeV1Client(restClient rest.Interface) *FakeV1 {
	return &FakeV1{}
}

func (c *FakeV1) Misc() v1.MiscInterface {
	return newMiscClient(nil)
}

func (c *FakeV1) CanaryConfig() v1.CanaryConfigInterface {
	return newCanaryConfigClient(nil)
}

func (c *FakeV1) Environment() v1.EnvironmentInterface {
	return newEnvironmentClient(nil)
}

func (c *FakeV1) Function() v1.FunctionInterface {
	return newFunctionClient(nil)
}

func (c *FakeV1) HTTPTrigger() v1.HTTPTriggerInterface {
	return newHTTPTriggerClient(nil)
}

func (c *FakeV1) KubeWatcher() v1.KubeWatcherInterface {
	return newKubeWatcher(nil)
}

func (c *FakeV1) MessageQueueTrigger() v1.MessageQueueTriggerInterface {
	return newMessageQueueTrigger(nil)
}

func (c *FakeV1) Package() v1.PackageInterface {
	return newPackageClient(nil)
}

func (c *FakeV1) TimeTrigger() v1.TimeTriggerInterface {
	return newTimeTriggerClient(nil)
}
