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
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/fission/fission"
	"github.com/fission/fission/cache"
	"github.com/fission/fission/crd"
)

type (
	// functionReferenceResolver provides a resolver to turn a function
	// reference into a resolveResult
	functionReferenceResolver struct {
		// FunctionReference -> function metadata
		refCache *cache.Cache

		stopCh           chan struct{}
		store            k8sCache.Store
		funcVersionStore k8sCache.Store
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

func makeFunctionReferenceResolver(store k8sCache.Store, funcVersionStore k8sCache.Store) *functionReferenceResolver {
	frr := &functionReferenceResolver{
		refCache:         cache.MakeCache(time.Minute, 0),
		store:            store,
		funcVersionStore: funcVersionStore,
	}
	return frr
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
	case fission.FunctionReferenceTypeFunctionVersion:
		rr, err = frr.resolveByVersion(namespace, fr.Name, fr.Version)
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
	obj, isExist, err := frr.store.Get(&crd.Function{
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

	f := obj.(*crd.Function)
	rr := resolveResult{
		resolveResultType: resolveResultSingleFunction,
		functionMetadata:  &f.Metadata,
	}
	return &rr, nil
}

// resolveByVersion looks up function by name and version in a namespace.
func (frr *functionReferenceResolver) resolveByVersion(namespace, name string, version string) (*resolveResult, error) {
	if version == "" {
		return nil, fmt.Errorf("Version cannot be empty for function:%s to be able to resolveByVersion", name)
	}

	resolvedVersion := version

	// when the version = latest, resolve to the last version created for this function to the actual version number.
	if strings.EqualFold(version, "latest") {
		obj, isExist, err := frr.funcVersionStore.Get(&crd.FunctionVersion{
			Metadata: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
		})
		if err != nil {
			return nil, err
		}
		if !isExist {
			return nil, fmt.Errorf("functionVersion object %v does not exist in store", name)
		}

		functionVersion := obj.(*crd.FunctionVersion)
		// A bit of explanation here: functionVersion.Spec.Versions will always reflect the versions present on the cluster for this function at this moment.
		// Every time a new version for a function is created, it's appended to the list. Every time a version is deleted, it's removed from the list.
		// So, in a way the list maintains a chronological order of versions created for a function and the latest version of a function will
		// be the last element in the list.
		resolvedVersion = functionVersion.Spec.Versions[len(functionVersion.Spec.Versions)-1]
	}

	log.Debugf("resolvedVersion for function: %s is %s", name, resolvedVersion)

	// get function from cache
	obj, isExist, err := frr.store.Get(&crd.Function{
		Metadata: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("%s-%s", name, resolvedVersion),
		},
	})
	if err != nil {
		return nil, err
	}
	if !isExist {
		return nil, fmt.Errorf("function %s-%s does not exist", name, version)
	}

	f := obj.(*crd.Function)
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
