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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
)

type (
	HTTPTriggerInterface interface {
		Create(*fv1.HTTPTrigger) (*fv1.HTTPTrigger, error)
		Get(name string) (*fv1.HTTPTrigger, error)
		Update(*fv1.HTTPTrigger) (*fv1.HTTPTrigger, error)
		Delete(name string, options *metav1.DeleteOptions) error
		List(opts metav1.ListOptions) (*fv1.HTTPTriggerList, error)
		Watch(opts metav1.ListOptions) (watch.Interface, error)
	}

	httpTriggerClient struct {
		client    *rest.RESTClient
		namespace string
	}
)

func MakeHTTPTriggerInterface(crdClient *rest.RESTClient, namespace string) HTTPTriggerInterface {
	return &httpTriggerClient{
		client:    crdClient,
		namespace: namespace,
	}
}

func (c *httpTriggerClient) Create(obj *fv1.HTTPTrigger) (*fv1.HTTPTrigger, error) {
	var result fv1.HTTPTrigger
	err := c.client.Post().
		Resource("httptriggers").
		Namespace(c.namespace).
		Body(obj).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *httpTriggerClient) Get(name string) (*fv1.HTTPTrigger, error) {
	var result fv1.HTTPTrigger
	err := c.client.Get().
		Resource("httptriggers").
		Namespace(c.namespace).
		Name(name).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *httpTriggerClient) Update(obj *fv1.HTTPTrigger) (*fv1.HTTPTrigger, error) {
	var result fv1.HTTPTrigger
	err := c.client.Put().
		Resource("httptriggers").
		Namespace(c.namespace).
		Name(obj.Metadata.Name).
		Body(obj).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *httpTriggerClient) Delete(name string, opts *metav1.DeleteOptions) error {
	return c.client.Delete().
		Namespace(c.namespace).
		Resource("httptriggers").
		Name(name).
		Body(opts).
		Do().
		Error()
}

func (c *httpTriggerClient) List(opts metav1.ListOptions) (*fv1.HTTPTriggerList, error) {
	var result fv1.HTTPTriggerList
	err := c.client.Get().
		Namespace(c.namespace).
		Resource("httptriggers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Do().
		Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *httpTriggerClient) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	return c.client.Get().
		Prefix("watch").
		Namespace(c.namespace).
		Resource("httptriggers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Watch()
}
