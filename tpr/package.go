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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

type (
	PackageInterface interface {
		Create(*Package) (*Package, error)
		Get(name string) (*Package, error)
		Update(*Package) (*Package, error)
		Delete(name string, options *metav1.DeleteOptions) error
		List(opts metav1.ListOptions) (*PackageList, error)
		Watch(opts metav1.ListOptions) (watch.Interface, error)
	}

	packageClient struct {
		client    *rest.RESTClient
		namespace string
	}
)

func MakePackageInterface(tprClient *rest.RESTClient, namespace string) PackageInterface {
	return &packageClient{
		client:    tprClient,
		namespace: namespace,
	}
}

func (c *packageClient) Create(f *Package) (*Package, error) {
	var result Package
	err := c.client.Post().
		Resource("packages").
		Namespace(c.namespace).
		Body(f).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *packageClient) Get(name string) (*Package, error) {
	var result Package
	err := c.client.Get().
		Resource("packages").
		Namespace(c.namespace).
		Name(name).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *packageClient) Update(f *Package) (*Package, error) {
	var result Package
	err := c.client.Put().
		Resource("packages").
		Namespace(c.namespace).
		Name(f.Metadata.Name).
		Body(f).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *packageClient) Delete(name string, opts *metav1.DeleteOptions) error {
	return c.client.Delete().
		Namespace(c.namespace).
		Resource("packages").
		Name(name).
		Body(opts).
		Do().
		Error()
}

func (c *packageClient) List(opts metav1.ListOptions) (*PackageList, error) {
	var result PackageList
	err := c.client.Get().
		Namespace(c.namespace).
		Resource("packages").
		VersionedParams(&opts, scheme.ParameterCodec).
		Do().
		Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *packageClient) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	return c.client.Get().
		Prefix("watch").
		Namespace(c.namespace).
		Resource("packages").
		VersionedParams(&opts, scheme.ParameterCodec).
		Watch()
}
