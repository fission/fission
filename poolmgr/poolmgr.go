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

package poolmgr

import (
	"log"
	"strings"

	"github.com/dchest/uniuri"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/rest"

	controllerclient "github.com/fission/fission/controller/client"
)

// Get a kubernetes client using the pod's service account.  This only
// works when we're running inside a kubernetes cluster.
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

func StartPoolmgr(controllerUrl string, namespace string, port int) error {
	controllerUrl = strings.TrimSuffix(controllerUrl, "/")
	controllerClient := controllerclient.MakeClient(controllerUrl)

	kubernetesClient, err := getKubernetesClient()
	if err != nil {
		log.Printf("Failed to get kubernetes client: %v", err)
		return err
	}

	instanceId := uniuri.NewLen(8)
	cleanupOldPoolmgrResources(kubernetesClient, namespace, instanceId)

	fsCache := MakeFunctionServiceCache()
	gpm := MakeGenericPoolManager(controllerUrl, kubernetesClient, namespace, fsCache, instanceId)

	api := MakeAPI(gpm, controllerClient, fsCache)
	go api.Serve(port)

	return nil
}
