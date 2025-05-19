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

package util

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/utils"
)

const maxDuration time.Duration = 2000

// Port forward a free local port to a pod on the cluster. The pod is
// found in the specified namespace by labelSelector. The pod's port
// is found by looking for a service in the same namespace and using
// its targetPort. Once the port forward is started, wait for it to
// start accepting connections before returning.
func SetupPortForward(ctx context.Context, client cmd.Client, namespace, labelSelector string) (string, error) {
	console.Verbose(2, "Setting up port forward to %s in namespace %s",
		labelSelector, namespace)

	lcPort, err := utils.FindFreePort()
	if err != nil {
		return "", fmt.Errorf("error finding unused port: %w", err)
	}
	localPort := strconv.Itoa(lcPort)

	var waitDuration time.Duration = 50

	console.Verbose(2, "Waiting for local port %v", localPort)
	for {
		conn, _ := net.DialTimeout("tcp",
			net.JoinHostPort("", localPort), time.Millisecond*waitDuration)
		if conn != nil {
			conn.Close()
			waitDuration *= 2
			if waitDuration > maxDuration {
				waitDuration = maxDuration
			}
		} else {
			break
		}
	}

	console.Verbose(2, "Starting port forward from local port %v", localPort)

	readyC, _, err := runPortForward(ctx, client, labelSelector, localPort, namespace)
	if err != nil {
		fmt.Printf("Error forwarding to port %v: %s", localPort, err.Error())
		return "", err
	}

	<-readyC

	console.Verbose(2, "Waiting for port forward %v to start...", localPort)

	waitDuration = 50
	for {
		conn, err := net.DialTimeout("tcp",
			net.JoinHostPort("", localPort), time.Millisecond*waitDuration)
		if err != nil {
			console.Verbose(2, "Error dialing on local port %v: %s", localPort, err.Error())
			waitDuration *= 2
			if waitDuration > maxDuration {
				waitDuration = maxDuration
			}
		} else {
			conn.Close()
			break
		}
	}

	console.Verbose(2, "Port forward from local port %v started", localPort)

	return localPort, nil
}

// runPortForward creates a local port forward to the specified pod
func runPortForward(ctx context.Context, client cmd.Client, labelSelector string, localPort string, ns string) (chan struct{}, chan struct{}, error) {

	console.Verbose(2, "Connected to Kubernetes API")

	// if namespace is unset, try to find a pod in any namespace
	if len(ns) == 0 {
		ns = metav1.NamespaceAll
	}

	// get the pod; if there is more than one, ask the user to disambiguate
	podList, err := client.KubernetesClient.CoreV1().Pods(ns).
		List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, nil, fmt.Errorf("error getting pod for port-forwarding with label selector %v: %w", labelSelector, err)
	} else if len(podList.Items) == 0 {
		return nil, nil, fmt.Errorf("no available pod for port-forwarding with label selector %v", labelSelector)
	}

	nsList := make([]string, 0)
	namespaces := make(map[string][]*v1.Pod)

	// make a useful error message if there is more than one install
	if len(podList.Items) > 0 {
		for _, p := range podList.Items {
			if _, ok := namespaces[p.Namespace]; !ok {
				namespaces[p.Namespace] = []*v1.Pod{}
				nsList = append(nsList, p.Namespace)
			}
			namespaces[p.Namespace] = append(namespaces[p.Namespace], &p)
		}
		if len(nsList) > 1 {
			return nil, nil, fmt.Errorf("found %v fission installs, set FISSION_NAMESPACE to one of: %v",
				len(namespaces), strings.Join(nsList, " "))
		}
	}

	// there is at most one namespace in nsList,
	// use index 0 to get from it directly.
	ns = nsList[0]
	pods, ok := namespaces[ns]
	if !ok {
		return nil, nil, fmt.Errorf("error finding fission install within the given namespace %v, please check FISSION_NAMESPACE is set properly", ns)
	}

	var podName, podNameSpace string

	// make sure we establish the connection to a healthy pod
	for _, p := range pods {
		if utils.IsReadyPod(p) {
			podName = p.Name
			podNameSpace = p.Namespace
			break
		}
	}

	// get the service and the target port
	svcs, err := client.KubernetesClient.CoreV1().Services(podNameSpace).
		List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, nil, fmt.Errorf("error getting %v service: %w", labelSelector, err)
	}
	if len(svcs.Items) == 0 {
		return nil, nil, fmt.Errorf("service %v not found", labelSelector)
	}
	service := &svcs.Items[0]

	var targetPort string
	for _, servicePort := range service.Spec.Ports {
		targetPort = servicePort.TargetPort.String()
	}
	console.Verbose(2, "Connecting to port %v on pod %v/%v", targetPort, podNameSpace, podName)

	stopChannel := make(chan struct{}, 1)
	readyChannel := make(chan struct{})

	// create request URL
	req := client.KubernetesClient.CoreV1().RESTClient().Post().Resource("pods").
		Namespace(podNameSpace).Name(podName).SubResource("portforward")
	url := req.URL()

	// create ports slice
	portCombo := localPort + ":" + targetPort
	ports := []string{portCombo}

	// actually start the port-forwarding process here
	transport, upgrader, err := spdy.RoundTripperFor(client.RestConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to Fission service on Kubernetes")
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url)

	outStream := os.Stdout
	if console.Verbosity < 2 {
		outStream = nil
	}
	fw, err := portforward.New(dialer, ports, stopChannel, readyChannel, outStream, os.Stderr)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating port forwarder: %w", err)
	}

	go func() {
		console.Verbose(2, "Starting port forwarder")
		err := fw.ForwardPorts()
		if err != nil {
			console.Verbose(2, "Error forwarding ports: %v", err)
		}
	}()

	return readyChannel, stopChannel, nil
}
