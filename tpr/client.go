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

package tpr

import (
	"errors"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type (
	FissionClient struct {
		tprClient *rest.RESTClient
	}
)

// Get a kubernetes client using the kubeconfig file at the
// environment var $KUBECONFIG, or an in-cluster config if that's
// undefined.
func GetKubernetesClient() (*rest.Config, *kubernetes.Clientset, error) {
	var config *rest.Config
	var err error

	// get the config, either from kubeconfig or using our
	// in-cluster service account
	kubeConfig := os.Getenv("KUBECONFIG")
	if len(kubeConfig) != 0 {
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			return nil, nil, err
		}
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, err
		}
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	return config, clientset, nil
}

// GetTprClient gets a TPR client config
func GetTprClient(config *rest.Config) (*rest.RESTClient, error) {
	// mutate config to add our types
	configureClient(config)

	// make a REST client with that config
	return rest.RESTClientFor(config)
}

// configureClient sets up a REST client for Fission TPR types.
//
// This is copied from the client-go TPR example.  (I don't understand
// all of it completely.)  It registers our types with the global API
// "scheme" (api.Scheme), which keeps a directory of types [I guess so
// it can use the string in the Kind field to make a Go object?].  It
// also puts the fission TPR types under a "group version" which we
// create for our TPRs types.
func configureClient(config *rest.Config) {
	groupversion := schema.GroupVersion{
		Group:   "fission.io",
		Version: "v1",
	}
	config.GroupVersion = &groupversion
	config.APIPath = "/apis"
	config.ContentType = runtime.ContentTypeJSON
	config.NegotiatedSerializer = serializer.DirectCodecFactory{CodecFactory: scheme.Codecs}

	schemeBuilder := runtime.NewSchemeBuilder(
		func(scheme *runtime.Scheme) error {
			scheme.AddKnownTypes(
				groupversion,
				&Function{},
				&FunctionList{},
				&metav1.ListOptions{},
				&metav1.DeleteOptions{},
			)
			scheme.AddKnownTypes(
				groupversion,
				&Environment{},
				&EnvironmentList{},
				&metav1.ListOptions{},
				&metav1.DeleteOptions{},
			)
			scheme.AddKnownTypes(
				groupversion,
				&Httptrigger{},
				&HttptriggerList{},
				&metav1.ListOptions{},
				&metav1.DeleteOptions{},
			)
			scheme.AddKnownTypes(
				groupversion,
				&Kuberneteswatchtrigger{},
				&KuberneteswatchtriggerList{},
				&metav1.ListOptions{},
				&metav1.DeleteOptions{},
			)
			scheme.AddKnownTypes(
				groupversion,
				&Timetrigger{},
				&TimetriggerList{},
				&metav1.ListOptions{},
				&metav1.DeleteOptions{},
			)
			scheme.AddKnownTypes(
				groupversion,
				&Messagequeuetrigger{},
				&MessagequeuetriggerList{},
				&metav1.ListOptions{},
				&metav1.DeleteOptions{},
			)
			scheme.AddKnownTypes(
				groupversion,
				&Package{},
				&PackageList{},
				&metav1.ListOptions{},
				&metav1.DeleteOptions{},
			)
			return nil
		})
	schemeBuilder.AddToScheme(scheme.Scheme)
}

func waitForTPRs(tprClient *rest.RESTClient) error {
	start := time.Now()
	for {
		fi := MakeFunctionInterface(tprClient, metav1.NamespaceDefault)
		_, err := fi.List(metav1.ListOptions{})
		if err != nil {
			time.Sleep(100 * time.Millisecond)
		} else {
			return nil
		}

		if time.Now().Sub(start) > 30*time.Second {
			return errors.New("timeout waiting for TPRs")
		}
	}
}

func MakeFissionClient() (*FissionClient, *kubernetes.Clientset, error) {
	config, kubeClient, err := GetKubernetesClient()
	if err != nil {
		return nil, nil, err
	}
	tprClient, err := GetTprClient(config)
	if err != nil {
		return nil, nil, err
	}
	fc := &FissionClient{
		tprClient: tprClient,
	}
	return fc, kubeClient, nil
}

func (fc *FissionClient) Functions(ns string) FunctionInterface {
	return MakeFunctionInterface(fc.tprClient, ns)
}
func (fc *FissionClient) Environments(ns string) EnvironmentInterface {
	return MakeEnvironmentInterface(fc.tprClient, ns)
}
func (fc *FissionClient) Httptriggers(ns string) HttptriggerInterface {
	return MakeHttptriggerInterface(fc.tprClient, ns)
}
func (fc *FissionClient) Kuberneteswatchtriggers(ns string) KuberneteswatchtriggerInterface {
	return MakeKuberneteswatchtriggerInterface(fc.tprClient, ns)
}
func (fc *FissionClient) Timetriggers(ns string) TimetriggerInterface {
	return MakeTimetriggerInterface(fc.tprClient, ns)
}
func (fc *FissionClient) Messagequeuetriggers(ns string) MessagequeuetriggerInterface {
	return MakeMessagequeuetriggerInterface(fc.tprClient, ns)
}
func (fc *FissionClient) Packages(ns string) PackageInterface {
	return MakePackageInterface(fc.tprClient, ns)
}

func (fc *FissionClient) WaitForTPRs() {
	waitForTPRs(fc.tprClient)
}
