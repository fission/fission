/*
Copyright The Fission Authors.

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

// Code generated by lister-gen. DO NOT EDIT.

package v1

import (
	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// TimeTriggerLister helps list TimeTriggers.
// All objects returned here must be treated as read-only.
type TimeTriggerLister interface {
	// List lists all TimeTriggers in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1.TimeTrigger, err error)
	// TimeTriggers returns an object that can list and get TimeTriggers.
	TimeTriggers(namespace string) TimeTriggerNamespaceLister
	TimeTriggerListerExpansion
}

// _timeTriggerLister implements the TimeTriggerLister interface.
type _timeTriggerLister struct {
	indexer cache.Indexer
}

// NewTimeTriggerLister returns a new TimeTriggerLister.
func NewTimeTriggerLister(indexer cache.Indexer) TimeTriggerLister {
	return &_timeTriggerLister{indexer: indexer}
}

// List lists all TimeTriggers in the indexer.
func (s *_timeTriggerLister) List(selector labels.Selector) (ret []*v1.TimeTrigger, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1.TimeTrigger))
	})
	return ret, err
}

// TimeTriggers returns an object that can list and get TimeTriggers.
func (s *_timeTriggerLister) TimeTriggers(namespace string) TimeTriggerNamespaceLister {
	return _timeTriggerNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// TimeTriggerNamespaceLister helps list and get TimeTriggers.
// All objects returned here must be treated as read-only.
type TimeTriggerNamespaceLister interface {
	// List lists all TimeTriggers in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1.TimeTrigger, err error)
	// Get retrieves the TimeTrigger from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1.TimeTrigger, error)
	TimeTriggerNamespaceListerExpansion
}

// _timeTriggerNamespaceLister implements the TimeTriggerNamespaceLister
// interface.
type _timeTriggerNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all TimeTriggers in the indexer for a given namespace.
func (s _timeTriggerNamespaceLister) List(selector labels.Selector) (ret []*v1.TimeTrigger, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1.TimeTrigger))
	})
	return ret, err
}

// Get retrieves the TimeTrigger from the indexer for a given namespace and name.
func (s _timeTriggerNamespaceLister) Get(name string) (*v1.TimeTrigger, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1.Resource("timetrigger"), name)
	}
	return obj.(*v1.TimeTrigger), nil
}
