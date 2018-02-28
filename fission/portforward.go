package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	//	"k8s.io/client-go/rest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
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

// runPortForward creates a local port forward to the specified pod
func runPortForward(kubeConfig string, labelSelector string, localPort string, fissionNamespace string) error {
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		fatal(fmt.Sprintf("Failed to connect to Kubernetes: %s", err))
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fatal(fmt.Sprintf("Failed to connect to Kubernetes: %s", err))
	}

	// get the pod; if there is more than one, always port-forward to the first.
	podList, err := clientset.CoreV1().Pods(fissionNamespace).
		List(meta_v1.ListOptions{LabelSelector: labelSelector})
	if err != nil || len(podList.Items) == 0 {
		fatal("Error getting controller pod for port-forwarding")
	}

	podName := podList.Items[0].Name
	podNameSpace := podList.Items[0].Namespace

	// get the service and the target port
	svcs, err := clientset.CoreV1().Services(podNameSpace).
		List(meta_v1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		fatal(fmt.Sprintf("Error getting %v service :%v", labelSelector, err.Error()))
	}
	if len(svcs.Items) == 0 {
		fatal(fmt.Sprintf("Service %v not found", labelSelector))
	}
	service := &svcs.Items[0]

	var targetPort string
	for _, servicePort := range service.Spec.Ports {
		targetPort = servicePort.TargetPort.String()
	}

	stopChannel := make(chan struct{}, 1)
	readyChannel := make(chan struct{})

	// create request URL
	req := clientset.CoreV1Client.RESTClient().Post().Resource("pods").
		Namespace(podNameSpace).Name(podName).SubResource("portforward")
	url := req.URL()

	// create ports slice
	portCombo := localPort + ":" + targetPort
	ports := []string{portCombo}

	// actually start the port-forwarding process here
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

// Port forward a free local port to a pod on the cluster. The pod is
// found in the specified namespace by labelSelector. The pod's port
// is found by looking for a service in the same namespace and using
// its targetPort. Once the port forward is started, wait for it to
// start accepting connections before returning.
func setupPortForward(kubeConfig, namespace, labelSelector string) string {
	localPort, err := findFreePort()
	if err != nil {
		fatal(fmt.Sprintf("Error finding unused port :%v", err.Error()))
	}

	for {
		conn, _ := net.DialTimeout("tcp",
			net.JoinHostPort("", localPort), time.Millisecond)
		if conn != nil {
			conn.Close()
		} else {
			break
		}
		time.Sleep(time.Millisecond * 50)
	}

	go func() {
		err := runPortForward(kubeConfig, labelSelector, localPort, namespace)
		if err != nil {
			fatal(fmt.Sprintf("Error forwarding to controller port: %s", err.Error()))
		}
	}()

	for {
		conn, _ := net.DialTimeout("tcp",
			net.JoinHostPort("", localPort), time.Millisecond)
		if conn != nil {
			conn.Close()
			break
		}
		time.Sleep(time.Millisecond * 50)
	}

	return localPort
}
