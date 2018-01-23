package main

import (
	"fmt"
	"github.com/fission/fission/crd"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"net"
	"os"
	"strconv"
	"time"
)

func findFreePort() (string, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", err
	}

	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	file, err := listener.(*net.TCPListener).File()

	if err != nil {
		return "", nil
	}

	err = listener.Close()
	if err != nil {
		return "", err
	}

	err = file.Close()
	if err != nil {
		return "", err
	}

	return port, nil
}

func runportForward(serviceName string, localPort string) error {

	//KUBECONFIG needs to be set to the correct path i.e ~/.kube/config
	config, PodClient, _, err := crd.GetKubernetesClient()
	if err != nil {
		msg := fmt.Sprint("%v", err)
		fatal(msg)
	}

	//get the podname for the controller
	PodList, err := PodClient.CoreV1().Pods("").List(meta_v1.ListOptions{LabelSelector: "svc=" + serviceName})
	if err != nil {
		fatal("Error getting PodList with selector")
	}
	var podName string
	var podNameSpace string
	//there should only be one Pod in this list, the controller pod
	for _, item := range PodList.Items {

		podName = item.Name
		podNameSpace = item.Namespace
		break
	}
	fmt.Println(podName)

	if err != nil {
		msg := fmt.Sprintf("%v", err)
		fatal(msg)
	}

	//get the ControllerPort
	service, err := PodClient.CoreV1().Services(podNameSpace).Get(serviceName, meta_v1.GetOptions{})
	if err != nil {
		fatal(fmt.Sprintf("Error getting %v service :%v", serviceName, err))
	}
	var targetPort string
	for _, servicePort := range service.Spec.Ports {

		targetPort = servicePort.TargetPort.String()
	}

	StopChannel := make(chan struct{}, 1)
	ReadyChannel := make(chan struct{})

	//create request URL
	req := PodClient.CoreV1Client.RESTClient().Post().Resource("pods").Namespace(podNameSpace).Name(podName).SubResource("portforward")
	url := req.URL()

	//create ports slice
	portCombo := localPort + ":" + targetPort
	ports := []string{portCombo}

	//actually start the port-forwarding process here
	dialer, err := remotecommand.NewExecutor(config, "POST", url)
	if err != nil {
		msg := fmt.Sprintf("newexecutor errored out :%v", err)
		fatal(msg)
	}
	fw, err := portforward.New(dialer, ports, StopChannel, ReadyChannel, nil, os.Stderr)

	if err != nil {
		msg := fmt.Sprintf("portforward.new errored out :%v", err)
		fatal(msg)
	}
	return fw.ForwardPorts()
}
