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
	"strings"

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
		Namespace   string
		RestConfig  *rest.Config
	}
	Client struct {
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
	client := &Client{}
	var err error
	var cmdConfig clientcmd.ClientConfig
	var restConfig *rest.Config
	if len(opts.Namespace) > 0 {
		client.Namespace = opts.Namespace
	} else {
		cmdConfig, err = GetClientConfig(opts.KubeContext)
		if err != nil {
			console.Verbose(2, err.Error())
		}

		if cmdConfig != nil {
			namespace, _, err := cmdConfig.Namespace()
			if err != nil {
				return nil, err
			}
			client.Namespace = namespace
			console.Verbose(2, "Kubeconfig default namespace %q", namespace)
		}
	}

	if cmdConfig == nil {
		console.Verbose(2, "Using in-cluster config")
		restConfig, err = rest.InClusterConfig()
		if err != nil {
			return nil, err
		}

		client.Namespace = getInClusterConfigamespace()
		console.Verbose(2, "In-cluster config default namespace %q", client.Namespace)
	}

	if opts.RestConfig != nil {
		client.RestConfig = opts.RestConfig
	} else if cmdConfig != nil {
		restConfig, err := cmdConfig.ClientConfig()
		if err != nil {
			return nil, err
		}
		client.RestConfig = restConfig
	} else {
		client.RestConfig = restConfig
	}

	clientGen := crd.NewClientGeneratorWithRestConfig(client.RestConfig)
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

// Fetch default namespace for inClusterConfig
func getInClusterConfigamespace() string {
	// This way assumes you've set the POD_NAMESPACE environment variable using the downward API.
	// This check has to be done first for backwards compatibility with the way InClusterConfig was originally set up
	if ns, ok := os.LookupEnv("POD_NAMESPACE"); ok {
		return ns
	}

	// Fall back to the namespace associated with the service account token, if available
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}

	return "default"
}
