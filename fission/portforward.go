package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/fission/fission/crd"
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

func runportForward(serviceName string, localPort string, fissionNamespace string) error {
	//KUBECONFIG needs to be set to the correct path i.e ~/.kube/config
	config, podClient, _, err := crd.GetKubernetesClient()
	if err != nil {
		fatal(err.Error())
	}

	//get the podname for the controller
	podList, err := podClient.CoreV1().Pods(fissionNamespace).List(meta_v1.ListOptions{LabelSelector: "application=fission-api"})
	if err != nil || len(podList.Items) == 0 {
		fatal("Error getting controller pod for port-forwarding")
	}

	// if there are more than one pods, always port-forward to the first pod returned
	podName := podList.Items[0].Name
	podNameSpace := podList.Items[0].Namespace

	//get the ControllerPort
	service, err := podClient.CoreV1().Services(podNameSpace).Get(serviceName, meta_v1.GetOptions{})
	if err != nil {
		fatal(fmt.Sprintf("Error getting %v service :%v", serviceName, err.Error()))
	}

	var targetPort string
	for _, servicePort := range service.Spec.Ports {
		targetPort = servicePort.TargetPort.String()
	}

	stopChannel := make(chan struct{}, 1)
	readyChannel := make(chan struct{})

	//create request URL
	req := podClient.CoreV1Client.RESTClient().Post().Resource("pods").Namespace(podNameSpace).Name(podName).SubResource("portforward")
	url := req.URL()

	//create ports slice
	portCombo := localPort + ":" + targetPort
	ports := []string{portCombo}

	//actually start the port-forwarding process here
	dialer, err := remotecommand.NewExecutor(config, "POST", url)
	if err != nil {
		msg := fmt.Sprintf("newexecutor errored out :%v", err.Error())
		fatal(msg)
	}

	fw, err := portforward.New(dialer, ports, stopChannel, readyChannel, nil, os.Stderr)
	if err != nil {
		msg := fmt.Sprintf("portforward.new errored out :%v", err.Error())
		fatal(msg)
	}

	return fw.ForwardPorts()
}

func controllerPodPortForward(fissionNamespace string) string {
	localControllerPort, err := findFreePort()
	if err != nil {
		fatal(fmt.Sprintf("Error finding unused port :%v", err.Error()))
	}

	timeBefore := time.Now()
	for {
		conn, _ := net.DialTimeout("tcp", net.JoinHostPort("", localControllerPort), time.Millisecond)
		if conn != nil {
			conn.Close()
		} else {
			break
		}
		time.Sleep(time.Millisecond * 50)
	}

	timeAfter := time.Since(timeBefore)
	if timeAfter.Seconds()/1000 >= 100 {
		fatal(fmt.Sprintln("Lag in connecting to a free port on the localhost"))
	}

	go func() {
		err := runportForward("controller", localControllerPort, fissionNamespace)
		if err != nil {
			fatal(err.Error())
		}
	}()

	for {
		conn, _ := net.DialTimeout("tcp", net.JoinHostPort("", localControllerPort), time.Millisecond)
		if conn != nil {
			conn.Close()
			break
		}
		time.Sleep(time.Millisecond * 50)
	}

	return localControllerPort
}
