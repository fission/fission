// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package cluster provides access to a Fission installation for benchmarking:
// typed Kubernetes/Fission clients, router-internal HMAC signing, and
// Prometheus/pprof capture. Resource setup uses the version-stable typed
// clientset (not the in-process CLI), so the same binary can drive HEAD or a
// released control plane.
package cluster

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// Clients bundles the typed clients a benchmark run needs.
type Clients struct {
	RestConfig *rest.Config
	Fission    versioned.Interface
	Kube       kubernetes.Interface
}

// Connect builds Fission + Kubernetes clients. If kubeconfigPath is non-empty it
// is used directly; otherwise the standard resolution applies (KUBECONFIG env,
// then in-cluster config), matching the integration framework.
func Connect(kubeconfigPath string) (*Clients, error) {
	var (
		restConfig *rest.Config
		err        error
	)
	if kubeconfigPath != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		restConfig, err = ctrl.GetConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}

	cg := crd.NewClientGeneratorWithRestConfig(restConfig)
	fc, err := cg.GetFissionClient()
	if err != nil {
		return nil, fmt.Errorf("build fission client: %w", err)
	}
	kc, err := cg.GetKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	return &Clients{RestConfig: restConfig, Fission: fc, Kube: kc}, nil
}

// ServerVersion returns the cluster's Kubernetes git version (e.g. v1.34.8), or
// "" if it cannot be determined.
func (c *Clients) ServerVersion() string {
	v, err := c.Kube.Discovery().ServerVersion()
	if err != nil {
		return ""
	}
	return v.GitVersion
}
