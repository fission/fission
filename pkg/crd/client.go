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

package crd

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

type (
	ClientGeneratorInterface interface {
		GetFissionClient() (versioned.Interface, error)
		GetKubernetesClient() (kubernetes.Interface, error)
		GetApiExtensionsClient() (apiextensionsclient.Interface, error)
		GetMetricsClient() (metricsclient.Interface, error)
		GetDynamicClient() (dynamic.Interface, error)
	}

	ClientGenerator struct {
		restConfig *rest.Config
	}
)

func (cg *ClientGenerator) getRestConfig() (*rest.Config, error) {
	if cg.restConfig != nil {
		return cg.restConfig, nil
	}

	var err error
	cg.restConfig, err = config.GetConfig()
	if err != nil {
		return nil, err
	}
	return cg.restConfig, nil
}

func (cg *ClientGenerator) GetFissionClient() (versioned.Interface, error) {
	config, err := cg.getRestConfig()
	if err != nil {
		return nil, err
	}
	return versioned.NewForConfig(config)
}

func (cg *ClientGenerator) GetKubernetesClient() (kubernetes.Interface, error) {
	config, err := cg.getRestConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func (cg *ClientGenerator) GetApiExtensionsClient() (apiextensionsclient.Interface, error) {
	config, err := cg.getRestConfig()
	if err != nil {
		return nil, err
	}
	return apiextensionsclient.NewForConfig(config)
}

func (cg *ClientGenerator) GetMetricsClient() (metricsclient.Interface, error) {
	config, err := cg.getRestConfig()
	if err != nil {
		return nil, err
	}
	return metricsclient.NewForConfig(config)
}

func (cg *ClientGenerator) GetDynamicClient() (dynamic.Interface, error) {
	config, err := cg.getRestConfig()
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(config)
}

func NewClientGenerator() *ClientGenerator {
	return &ClientGenerator{}
}

func NewClientGeneratorWithRestConfig(restConfig *rest.Config) *ClientGenerator {
	return &ClientGenerator{restConfig: restConfig}
}

// WaitForCRDs does a timeout to check if CRDs have been installed
func WaitForCRDs(ctx context.Context, logger *zap.Logger, fissionClient versioned.Interface) error {
	logger.Info("Waiting for CRDs to be installed")
	defaultNs := utils.DefaultNSResolver().DefaultNamespace
	if defaultNs == "" {
		defaultNs = metav1.NamespaceDefault
	}
	start := time.Now()
	for {
		fi := fissionClient.CoreV1().Functions(defaultNs)
		_, err := fi.List(ctx, metav1.ListOptions{})
		if err != nil {
			time.Sleep(100 * time.Millisecond)
		} else {
			return nil
		}

		if time.Since(start) > 30*time.Second {
			return errors.New("timeout waiting for CRDs")
		}
	}
}
