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

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/tools/clientcmd"

	"github.com/fission/fission/controller/client"
)

const UI_INSTALL_URL = "https://github.com/fission/fission/blob/master/INSTALL.md#use-the-web-based-fission-ui-optional"

func fatal(msg string) {
	os.Stderr.WriteString(msg + "\n")
	os.Exit(1)
}

func getClient(serverUrl string) *client.Client {

	if len(serverUrl) == 0 {
		serverUrl = getFissionEnvVariable("controller")
	}

	if len(serverUrl) == 0 {
		fatal("Need --server or FISSION_URL set to your fission server. Or check kubernetes config.")
	}

	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0

	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}

	return client.MakeClient(serverUrl)
}

func checkErr(err error, msg string) {
	if err != nil {
		fatal(fmt.Sprintf("Failed to %v: %v", msg, err))
	}
}

// source from k8s client-go out-of-cluster-client-configuration example
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func getKubernetesClient() (*kubernetes.Clientset, error) {
	var kubeconfig string
	if home := homeDir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	} else {
		return nil, errors.New("Kube config is not found in home folder")
	}

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return clientset, nil
}

func getNodeIP(client *kubernetes.Clientset) (string, error) {
	nodes, err := client.Nodes().List(api.ListOptions{})
	if err != nil {
		return "", err
	}
	if len(nodes.Items) < 1 {
		return "", errors.New("No available nodes are found.")
	}
	for _, address := range nodes.Items[0].Status.Addresses {
		if address.Type == v1.NodeExternalIP {
			return address.Address, nil
		}
	}
	return "", errors.New("No available external ip found")
}

func getFissionEnvVariable(svcName string) string {
	k8sClientset, err := getKubernetesClient()
	if err != nil {
		fmt.Printf("Error occured getting k8s client: %v\n", err)
		return ""
	}

	controller, err2 := k8sClientset.Core().Services("fission").Get(svcName)
	if err2 != nil {
		fmt.Printf("Error occured getting fission: %v %v\n", svcName, err2)
		return ""
	}

	switch controller.Spec.Type {
	case v1.ServiceTypeNodePort:
		// TODO: find the port via name
		ip, err3 := getNodeIP(k8sClientset)
		if err3 != nil {
			panic(err3)
		}
		return fmt.Sprintf("http://%v:%v",
			ip, controller.Spec.Ports[0].NodePort)
	case v1.ServiceTypeLoadBalancer:
		if len(controller.Status.LoadBalancer.Ingress) == 0 {
			fmt.Println("The LoadBalancer is not ready yet, please retry later")
			return ""
		}
		ip := controller.Status.LoadBalancer.Ingress[0].IP
		// on AWS, sometimes the IP isn't set, and there's a hostname instead
		if ip == "" {
			ip = controller.Status.LoadBalancer.Ingress[0].Hostname
		}
		if ip == "" {
			fmt.Println("The ip of LoadBalancer is not ready yet, please retry later")
			return ""
		}
		return fmt.Sprintf("http://%v:%v",
			ip, controller.Spec.Ports[0].Port)
	default:
		fmt.Println("Fission controller service type should be NodePort or LoadBalancer")
	}
	return ""
}
