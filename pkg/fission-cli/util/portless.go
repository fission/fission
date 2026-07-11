// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	portless "github.com/sanketsudake/go-portless"
	portlessk8s "github.com/sanketsudake/go-portless/k8s"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
)

// The CLI's port-forward plane: one process-lifetime portless registry, one
// route + local bridge per (namespace, labelSelector), memoized so repeated
// Get*URL calls within a command share a forward. Each accepted local
// connection opens its own SPDY stream to a ready pod (re-resolved per dial),
// so a pod restart mid-command costs one retried dial instead of a dead
// tunnel. Everything dies with the process — the CLI is per-invocation.
var (
	pfMu      sync.Mutex
	pfReg     *portless.Registry
	pfBridges = map[string]string{}
)

// SetupPortForward forwards a kernel-assigned local port to the Fission
// service selected by labelSelector, and returns the local port once it is
// accepting connections. The pod's port is discovered from the matching
// Service's targetPort. Consumers keep receiving a plain dialable
// 127.0.0.1:<port> URL, so bare HTTP clients, SDK transports, and URLs
// printed for humans (archive get-url) all keep working unchanged.
func SetupPortForward(ctx context.Context, client cmd.Client, namespace, labelSelector string) (string, error) {
	console.Verbose(2, "Setting up port forward to %s in namespace %s", labelSelector, namespace)
	pfMu.Lock()
	defer pfMu.Unlock()

	key := namespace + "/" + labelSelector
	if port, ok := pfBridges[key]; ok {
		return port, nil
	}

	ns, targetPort, err := resolveFissionService(ctx, client.KubernetesClient, namespace, labelSelector)
	if err != nil {
		return "", err
	}

	backend, err := portlessk8s.PortForward(client.RestConfig,
		portlessk8s.LabelSelector(ns, labelSelector), portlessk8s.TargetPort(targetPort))
	if err != nil {
		return "", fmt.Errorf("error creating port-forward backend for %v: %w", labelSelector, err)
	}
	if pfReg == nil {
		pfReg = portless.New()
	}
	// Preserve the old contract: SetupPortForward returning means a working
	// tunnel. AddReady blocks until a pod accepts (bounded by ctx / the
	// route's ready timeout) and frees the name again on failure, so callers
	// — including humans handed a printed URL — get a diagnosable error here
	// instead of a silent first-request stall, and a later retry can re-Add.
	if _, err := pfReg.AddReady(ctx, key, backend); err != nil {
		return "", fmt.Errorf("error setting up %v port-forward: %w", labelSelector, err)
	}

	// The local bridge lets plain-URL consumers (bare HTTP clients, SDK
	// transports, URLs printed for humans) ride portless's per-dial readiness
	// and pod re-resolution. The listener lives for the process — the CLI is
	// per-invocation.
	l, err := pfReg.ListenLocal(key)
	if err != nil {
		// Deregister so a later retry of the same service can re-Add
		// instead of failing ErrRouteExists forever.
		_ = pfReg.Remove(ctx, key)
		return "", fmt.Errorf("error bridging %v port-forward locally: %w", labelSelector, err)
	}
	// ListenLocal binds "tcp" on loopback, so the Addr is always a *net.TCPAddr.
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	pfBridges[key] = port
	console.Verbose(2, "Port forward to %s ready on local port %v", labelSelector, port)
	return port, nil
}

// resolveFissionService locates the single Fission install matching
// labelSelector and returns its namespace and the Service targetPort to
// forward to. It preserves the CLI's disambiguation UX: an empty namespace
// searches everywhere, and matches across several namespaces are an error
// telling the user to set FISSION_NAMESPACE.
func resolveFissionService(ctx context.Context, kube kubernetes.Interface, namespace, labelSelector string) (string, intstr.IntOrString, error) {
	none := intstr.IntOrString{}
	ns := namespace
	if ns == "" {
		ns = metav1.NamespaceAll
	}
	podList, err := kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", none, fmt.Errorf("error getting pod for port-forwarding with label selector %v: %w", labelSelector, err)
	}
	if len(podList.Items) == 0 {
		return "", none, fmt.Errorf("no available pod for port-forwarding with label selector %v", labelSelector)
	}

	// A useful error message if pods span more than one install.
	nsSet := map[string]bool{}
	nsList := make([]string, 0, 1)
	for _, p := range podList.Items {
		if !nsSet[p.Namespace] {
			nsSet[p.Namespace] = true
			nsList = append(nsList, p.Namespace)
		}
	}
	if len(nsList) > 1 {
		return "", none, fmt.Errorf("found %v fission installs, set FISSION_NAMESPACE to one of: %v",
			len(nsList), strings.Join(nsList, " "))
	}
	ns = nsList[0]

	svcs, err := kube.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", none, fmt.Errorf("error getting %v service: %w", labelSelector, err)
	}
	if len(svcs.Items) == 0 {
		return "", none, fmt.Errorf("service %v not found", labelSelector)
	}
	// Historic behavior: the last Service port's targetPort wins.
	var targetPort intstr.IntOrString
	for _, servicePort := range svcs.Items[0].Spec.Ports {
		targetPort = servicePort.TargetPort
	}
	return ns, targetPort, nil
}
