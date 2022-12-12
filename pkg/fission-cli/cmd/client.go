/*
Copyright 2022 The Fission Authors.

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

package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

type (
	ClientOptions struct {
		KubeContext string
	}
	Client struct {
		Options          ClientOptions
		ClientConfig     clientcmd.ClientConfig
		RestConfig       *rest.Config
		FissionClientSet versioned.Interface
		KubernetesClient kubernetes.Interface
		Namespace        string
	}
)

func (c *Client) SetFissionClientset(fissionClientSet versioned.Interface) {
	c.FissionClientSet = fissionClientSet
}

func (c *Client) SetKubernetesClient(kubernetesClient kubernetes.Interface) {
	c.KubernetesClient = kubernetesClient
}

func getLoadingRules() (loadingRules *clientcmd.ClientConfigLoadingRules, err error) {
	loadingRules = clientcmd.NewDefaultClientConfigLoadingRules()

	kubeConfigPath := os.Getenv("KUBECONFIG")
	if len(kubeConfigPath) == 0 {
		var homeDir string
		usr, err := user.Current()
		if err != nil {
			// In case that user.Current() may be unable to work under some circumstances and return errors like
			// "user: Current not implemented on darwin/amd64" due to cross-compilation problem. (https://github.com/golang/go/issues/6376).
			// Instead of doing fatal here, we fallback to get home directory from the environment $HOME.
			console.Warn(fmt.Sprintf("Could not get the current user's directory (%s), fallback to get it from env $HOME", err))
			homeDir = os.Getenv("HOME")
		} else {
			homeDir = usr.HomeDir
		}
		kubeConfigPath = filepath.Join(homeDir, ".kube", "config")

		if _, err := os.Stat(kubeConfigPath); os.IsNotExist(err) {
			return nil, errors.New("couldn't find kubeconfig file. " +
				"Set the KUBECONFIG environment variable to your kubeconfig's path.")
		}
		loadingRules.ExplicitPath = kubeConfigPath
		console.Verbose(2, "Using kubeconfig from %q", kubeConfigPath)
	} else {
		console.Verbose(2, "Using kubeconfig from environment %q", kubeConfigPath)
	}
	return loadingRules, nil
}

func GetClientConfig(kubeContext string) (clientcmd.ClientConfig, error) {
	loadingRules, err := getLoadingRules()
	if err != nil {
		return nil, err
	}
	overrides := &clientcmd.ConfigOverrides{}
	if len(kubeContext) > 0 {
		console.Verbose(2, "Using kubeconfig context %q", kubeContext)
		overrides.CurrentContext = kubeContext
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides), nil
}

func NewClient(opts ClientOptions) (*Client, error) {
	client := &Client{
		Options: opts,
	}
	cmdConfig, err := GetClientConfig(opts.KubeContext)
	if err != nil {
		return nil, err
	}
	client.ClientConfig = cmdConfig

	namespace, _, err := cmdConfig.Namespace()
	if err != nil {
		return nil, err
	}
	client.Namespace = namespace
	console.Verbose(2, "Kubeconfig default namespace %q", namespace)

	restConfig, err := cmdConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	client.RestConfig = restConfig

	clientGen := crd.NewClientGeneratorWithRestConfig(restConfig)
	clientset, err := clientGen.GetKubernetesClient()
	if err != nil {
		return nil, err
	}
	client.KubernetesClient = clientset

	fissionClientset, err := clientGen.GetFissionClient()
	if err != nil {
		return nil, err
	}
	client.FissionClientSet = fissionClientset

	return client, nil
}
