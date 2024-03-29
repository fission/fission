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

// PackageLister helps list Packages.
// All objects returned here must be treated as read-only.
type PackageLister interface {
	// List lists all Packages in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1.Package, err error)
	// Packages returns an object that can list and get Packages.
	Packages(namespace string) PackageNamespaceLister
	PackageListerExpansion
}

// packageLister implements the PackageLister interface.
type packageLister struct {
	indexer cache.Indexer
}

// NewPackageLister returns a new PackageLister.
func NewPackageLister(indexer cache.Indexer) PackageLister {
	return &packageLister{indexer: indexer}
}

// List lists all Packages in the indexer.
func (s *packageLister) List(selector labels.Selector) (ret []*v1.Package, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1.Package))
	})
	return ret, err
}

// Packages returns an object that can list and get Packages.
func (s *packageLister) Packages(namespace string) PackageNamespaceLister {
	return packageNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// PackageNamespaceLister helps list and get Packages.
// All objects returned here must be treated as read-only.
type PackageNamespaceLister interface {
	// List lists all Packages in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1.Package, err error)
	// Get retrieves the Package from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1.Package, error)
	PackageNamespaceListerExpansion
}

// packageNamespaceLister implements the PackageNamespaceLister
// interface.
type packageNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all Packages in the indexer for a given namespace.
func (s packageNamespaceLister) List(selector labels.Selector) (ret []*v1.Package, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1.Package))
	})
	return ret, err
}

// Get retrieves the Package from the indexer for a given namespace and name.
func (s packageNamespaceLister) Get(name string) (*v1.Package, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1.Resource("package"), name)
	}
	return obj.(*v1.Package), nil
}
