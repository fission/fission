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
	TimeTriggerInterface interface {
		Create(*fv1.TimeTrigger) (*fv1.TimeTrigger, error)
		Get(name string) (*fv1.TimeTrigger, error)
		Update(*fv1.TimeTrigger) (*fv1.TimeTrigger, error)
		Delete(name string, options *metav1.DeleteOptions) error
		List(opts metav1.ListOptions) (*fv1.TimeTriggerList, error)
		Watch(opts metav1.ListOptions) (watch.Interface, error)
	}

	timeTriggerClient struct {
		client    *rest.RESTClient
		namespace string
	}
)

func MakeTimeTriggerInterface(crdClient *rest.RESTClient, namespace string) TimeTriggerInterface {
	return &timeTriggerClient{
		client:    crdClient,
		namespace: namespace,
	}
}

func (fc *timeTriggerClient) Create(f *fv1.TimeTrigger) (*fv1.TimeTrigger, error) {
	var result fv1.TimeTrigger
	err := fc.client.Post().
		Resource("timetriggers").
		Namespace(fc.namespace).
		Body(f).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (fc *timeTriggerClient) Get(name string) (*fv1.TimeTrigger, error) {
	var result fv1.TimeTrigger
	err := fc.client.Get().
		Resource("timetriggers").
		Namespace(fc.namespace).
		Name(name).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (fc *timeTriggerClient) Update(f *fv1.TimeTrigger) (*fv1.TimeTrigger, error) {
	var result fv1.TimeTrigger
	err := fc.client.Put().
		Resource("timetriggers").
		Namespace(fc.namespace).
		Name(f.Metadata.Name).
		Body(f).
		Do().Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (fc *timeTriggerClient) Delete(name string, opts *metav1.DeleteOptions) error {
	return fc.client.Delete().
		Namespace(fc.namespace).
		Resource("timetriggers").
		Name(name).
		Body(opts).
		Do().
		Error()
}

func (fc *timeTriggerClient) List(opts metav1.ListOptions) (*fv1.TimeTriggerList, error) {
	var result fv1.TimeTriggerList
	err := fc.client.Get().
		Namespace(fc.namespace).
		Resource("timetriggers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Do().
		Into(&result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (fc *timeTriggerClient) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	return fc.client.Get().
		Prefix("watch").
		Namespace(fc.namespace).
		Resource("timetriggers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Watch()
}
