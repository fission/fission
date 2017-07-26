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
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/watch"
	"k8s.io/client-go/1.5/rest"
)

type (
	EnvironmentInterface interface {
		Create(*Environment) (*Environment, error)
		Get(name string) (*Environment, error)
		Update(*Environment) (*Environment, error)
		Delete(name string, options *api.DeleteOptions) error
		List(opts api.ListOptions) (*EnvironmentList, error)
		Watch(opts api.ListOptions) (watch.Interface, error)
	}

	environmentClient struct {
		client    *rest.RESTClient
		namespace string
	}
)

func MakeEnvironmentInterface(tprClient *rest.RESTClient, namespace string) EnvironmentInterface {
	return &environmentClient{
		client:    tprClient,
		namespace: namespace,
	}
}

func (ec *environmentClient) Create(e *Environment) (*Environment, error) {
	var result Environment
	err := ec.client.Post().
		Resource("environments").
		Namespace(ec.namespace).
		Body(e).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (ec *environmentClient) Get(name string) (*Environment, error) {
	var result Environment
	err := ec.client.Get().
		Resource("environments").
		Namespace(ec.namespace).
		Name(name).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (ec *environmentClient) Update(e *Environment) (*Environment, error) {
	var result Environment
	err := ec.client.Put().
		Resource("environments").
		Namespace(ec.namespace).
		Name(e.Metadata.Name).
		Body(e).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (ec *environmentClient) Delete(name string, opts *api.DeleteOptions) error {
	return ec.client.Delete().
		Namespace(ec.namespace).
		Resource("environments").
		Name(name).
		Body(opts).
		Do().
		Error()
}

func (ec *environmentClient) List(opts api.ListOptions) (*EnvironmentList, error) {
	var result EnvironmentList
	err := ec.client.Get().
		Namespace(ec.namespace).
		Resource("environments").
		VersionedParams(&opts, api.ParameterCodec).
		Do().
		Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (ec *environmentClient) Watch(opts api.ListOptions) (watch.Interface, error) {
	return ec.client.Get().
		Prefix("watch").
		Namespace(ec.namespace).
		Resource("environments").
		VersionedParams(&opts, api.ParameterCodec).
		Watch()
}
