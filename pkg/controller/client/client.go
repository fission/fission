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

package client

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/controller/client/rest"
	v1 "github.com/fission/fission/pkg/controller/client/v1"
)

type (
	Interface interface {
		V1() v1.V1Interface
		ServerURL() string
		KubeClient() *kubernetes.Clientset
		DynamicClient() dynamic.Interface
	}

	Clientset struct {
		restClient    rest.Interface
		v1            v1.V1Interface
		kubeClient    *kubernetes.Clientset
		dynamicClient dynamic.Interface
	}
)

func MakeClientset(restClient rest.Interface, kubeClient *kubernetes.Clientset, dynamicClient dynamic.Interface) Interface {
	return &Clientset{
		restClient:    restClient,
		v1:            v1.MakeV1Client(restClient),
		kubeClient:    kubeClient,
		dynamicClient: dynamicClient,
	}
}

func (c *Clientset) V1() v1.V1Interface {
	return c.v1
}

func (c *Clientset) ServerURL() string {
	return c.restClient.ServerURL()
}

func (c *Clientset) DynamicClient() dynamic.Interface {
	return c.dynamicClient
}

func (c *Clientset) KubeClient() *kubernetes.Clientset {
	return c.kubeClient
}
