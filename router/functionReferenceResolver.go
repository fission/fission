/*
Copyright 2017 The Fission Authors.

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

package router

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/fission/fission"
	"github.com/fission/fission/cache"
	"github.com/fission/fission/crd"
)

type (
	// functionReferenceResolver provides a resolver to turn a function
	// reference into a resolveResult
	functionReferenceResolver struct {
		fissionClient *crd.FissionClient

		// FunctionReference -> function metadata
		refCache *cache.Cache

		stopCh chan struct{}
		store  k8sCache.Store
	}

	resolveResultType int

	// resolveResult is the result of resolving a function reference; for now
	// it's just the metadata of one function, but in the future could support
	// a distribution of requests across two functions.
	resolveResult struct {
		resolveResultType
		functionMetadata *metav1.ObjectMeta
	}

	// namespacedFunctionReference is just a function reference plus a
	// namespace. Since a function reference works on names, it's only
	// meaningful within a namespace.
	namespacedFunctionReference struct {
		namespace         string
		functionReference fission.FunctionReference
	}
)

const (
	resolveResultSingleFunction = iota
)

func makeFunctionReferenceResolver(fissionClient *crd.FissionClient) *functionReferenceResolver {
	frr := &functionReferenceResolver{
		fissionClient: fissionClient,
		refCache:      cache.MakeCache(time.Minute, 0),
	}
	return frr
}

// Sync starts syncing tpr function resources from k8s api server
func (frr *functionReferenceResolver) Sync(tprClient *rest.RESTClient) {
	stopCh := make(chan struct{})
	store, controller := makeK8SCache(tprClient)
	frr.stopCh = stopCh
	frr.store = store
	go controller.Run(stopCh)
}

// Stop stops tpr resources syncing
func (frr *functionReferenceResolver) Stop() {
	frr.stopCh <- struct{}{}
}

func makeK8SCache(tprClient *rest.RESTClient) (k8sCache.Store, k8sCache.Controller) {
	watchlist := k8sCache.NewListWatchFromClient(tprClient, "functions", metav1.NamespaceDefault, fields.Everything())
	listWatch := &k8sCache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return watchlist.List(options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return watchlist.Watch(options)
		},
	}
	resyncPeriod := 30 * time.Second
	return k8sCache.NewInformer(listWatch, &tpr.Function{}, resyncPeriod,
		k8sCache.ResourceEventHandlerFuncs{})
}

// resolve translates a namespace and a function reference to resolveResult.
// The resolveResult for now is just a function's metadata. In the future, some
// function ref types may resolve to two functions rather than just one
// (e.g. for incremental deployment), which will make the resolveResult a bit
// more complex.
func (frr *functionReferenceResolver) resolve(namespace string, fr *fission.FunctionReference) (*resolveResult, error) {
	nfr := namespacedFunctionReference{
		namespace:         namespace,
		functionReference: *fr,
	}

	// check cache
	rrInt, err := frr.refCache.Get(nfr)
	if err == nil {
		result := rrInt.(resolveResult)
		return &result, nil
	}

	// resolve on cache miss
	var rr *resolveResult

	switch fr.Type {
	case fission.FunctionReferenceTypeFunctionName:
		rr, err = frr.resolveByName(namespace, fr.Name)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("Unrecognized function reference type %v", fr.Type)
	}

	// cache resolve result
	frr.refCache.Set(nfr, *rr)

	return rr, nil
}

// resolveByName simply looks up function by name in a namespace.
func (frr *functionReferenceResolver) resolveByName(namespace, name string) (*resolveResult, error) {
	// get function from cache
	obj, isExist, err := frr.store.Get(&tpr.Function{
		Metadata: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	})
	if err != nil {
		return nil, err
	}
	if !isExist {
		return nil, fmt.Errorf("function %v does not exist", name)
	}

	f := obj.(*tpr.Function)
	rr := resolveResult{
		resolveResultType: resolveResultSingleFunction,
		functionMetadata:  &f.Metadata,
	}
	return &rr, nil
}

func (frr *functionReferenceResolver) delete(namespace string, fr *fission.FunctionReference) error {
	nfr := namespacedFunctionReference{
		namespace:         namespace,
		functionReference: *fr,
	}
	return frr.refCache.Delete(nfr)
}

func (frr *functionReferenceResolver) copy() map[namespacedFunctionReference]resolveResult {
	cache := make(map[namespacedFunctionReference]resolveResult)
	for k, v := range frr.refCache.Copy() {
		key := k.(namespacedFunctionReference)
		val := v.(resolveResult)
		cache[key] = val
	}
	return cache
}
