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
	"log"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/publisher"
)

// Get a kubernetes client using the pod's service account.
func getKubernetesClient() (*kubernetes.Clientset, error) {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Error getting kubernetes client config: %v", err)
		return nil, err
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Error getting kubernetes client: %v", err)
		return nil, err
	}

	return clientset, nil
}

func Start(controllerUrl string, routerUrl string) error {
	kubeClient, err := getKubernetesClient()
	if err != nil {
		return err
	}
	poster := publisher.MakeWebhookPublisher(routerUrl)
	kubeWatch := MakeKubeWatcher(kubeClient, poster)

	MakeWatchSync(client.MakeClient(controllerUrl), kubeWatch)

	return nil
}
