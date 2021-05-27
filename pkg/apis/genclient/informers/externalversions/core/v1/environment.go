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

// Code generated by informer-gen. DO NOT EDIT.

package v1

import (
	"context"
	time "time"

	corev1 "github.com/fission/fission/pkg/apis/core/v1"
	versioned "github.com/fission/fission/pkg/apis/genclient/clientset/versioned"
	internalinterfaces "github.com/fission/fission/pkg/apis/genclient/informers/externalversions/internalinterfaces"
	v1 "github.com/fission/fission/pkg/apis/genclient/listers/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// EnvironmentInformer provides access to a shared informer and lister for
// Environments.
type EnvironmentInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1.EnvironmentLister
}

type _environmentInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewEnvironmentInformer constructs a new informer for Environment type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewEnvironmentInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredEnvironmentInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredEnvironmentInformer constructs a new informer for Environment type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredEnvironmentInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.CoreV1().Environments(namespace).List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.CoreV1().Environments(namespace).Watch(context.TODO(), options)
			},
		},
		&corev1.Environment{},
		resyncPeriod,
		indexers,
	)
}

func (f *_environmentInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredEnvironmentInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *_environmentInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&corev1.Environment{}, f.defaultInformer)
}

func (f *_environmentInformer) Lister() v1.EnvironmentLister {
	return v1.NewEnvironmentLister(f.Informer().GetIndexer())
}
