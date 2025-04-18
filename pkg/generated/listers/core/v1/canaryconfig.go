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
	corev1 "github.com/fission/fission/pkg/apis/core/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	listers "k8s.io/client-go/listers"
	cache "k8s.io/client-go/tools/cache"
)

// CanaryConfigLister helps list CanaryConfigs.
// All objects returned here must be treated as read-only.
type CanaryConfigLister interface {
	// List lists all CanaryConfigs in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*corev1.CanaryConfig, err error)
	// CanaryConfigs returns an object that can list and get CanaryConfigs.
	CanaryConfigs(namespace string) CanaryConfigNamespaceLister
	CanaryConfigListerExpansion
}

// canaryConfigLister implements the CanaryConfigLister interface.
type canaryConfigLister struct {
	listers.ResourceIndexer[*corev1.CanaryConfig]
}

// NewCanaryConfigLister returns a new CanaryConfigLister.
func NewCanaryConfigLister(indexer cache.Indexer) CanaryConfigLister {
	return &canaryConfigLister{listers.New[*corev1.CanaryConfig](indexer, corev1.Resource("canaryconfig"))}
}

// CanaryConfigs returns an object that can list and get CanaryConfigs.
func (s *canaryConfigLister) CanaryConfigs(namespace string) CanaryConfigNamespaceLister {
	return canaryConfigNamespaceLister{listers.NewNamespaced[*corev1.CanaryConfig](s.ResourceIndexer, namespace)}
}

// CanaryConfigNamespaceLister helps list and get CanaryConfigs.
// All objects returned here must be treated as read-only.
type CanaryConfigNamespaceLister interface {
	// List lists all CanaryConfigs in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*corev1.CanaryConfig, err error)
	// Get retrieves the CanaryConfig from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*corev1.CanaryConfig, error)
	CanaryConfigNamespaceListerExpansion
}

// canaryConfigNamespaceLister implements the CanaryConfigNamespaceLister
// interface.
type canaryConfigNamespaceLister struct {
	listers.ResourceIndexer[*corev1.CanaryConfig]
}
