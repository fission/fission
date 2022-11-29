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

package router

import (
	"fmt"
	"net/url"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cache "k8s.io/client-go/tools/cache"
)

type (
	functionServiceMap struct {
		logger *zap.Logger
		cache  cache.Store // map[metadataKey]*url.URL
	}

	// metav1.ObjectMeta is not hashable, so we make a hashable copy
	// of the subset of its fields that are identifiable.
	functionServiceMapObj struct {
		meta metav1.ObjectMeta
		url  url.URL
	}
)

func makeFunctionServiceMap(logger *zap.Logger, expiry time.Duration) *functionServiceMap {
	return &functionServiceMap{
		logger: logger.Named("function_service_map"),
		cache:  cache.NewTTLStore(keyFromMetadata, expiry),
	}
}

type ExplicitKey string

func keyFromMetadata(obj interface{}) (string, error) {

	mk, ok := obj.(functionServiceMapObj)
	if !ok {
		return "", fmt.Errorf("expected functionServiceMapObj, got %T", obj)
	}
	return fmt.Sprintf("%s/%s/%s", mk.meta.Name, mk.meta.Namespace, mk.meta.ResourceVersion), nil
}

func (fmap *functionServiceMap) lookup(f metav1.ObjectMeta) (*url.URL, error) {
	item, exists, err := fmap.cache.Get(functionServiceMapObj{meta: f})
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("error looking up function %s/%s/%s", f.Name, f.Namespace, f.ResourceVersion)
	}
	u, ok := item.(functionServiceMapObj)
	if !ok {
		return nil, fmt.Errorf("expected functionServiceMapObj, got %T", item)
	}
	return &u.url, nil
}

func (fmap *functionServiceMap) assign(f metav1.ObjectMeta, serviceURL url.URL) {
	old, exists, err := fmap.cache.Get(functionServiceMapObj{meta: f})
	if err != nil {
		fmap.logger.Error("error looking up function", zap.Error(err))
	}
	if exists {
		u, ok := old.(functionServiceMapObj)
		if !ok {
			fmap.logger.Error("expected functionServiceMapObj, got %T", zap.Any("old", old))
		}
		if u.url.String() == serviceURL.String() {
			// No change, so don't update the cache.
			return
		}
		fmap.logger.Error("function already exists in functionServiceMap, overwriting",
			zap.String("function", f.Name),
			zap.String("namespace", f.Namespace),
			zap.String("resourceVersion", f.ResourceVersion))
		// ignore error
	} else {
		err := fmap.cache.Add(functionServiceMapObj{meta: f, url: serviceURL})
		if err != nil {
			fmap.logger.Error("error adding function to functionServiceMap", zap.Error(err))
		}
	}
}

func (fmap *functionServiceMap) remove(f metav1.ObjectMeta) error {
	return fmap.cache.Delete(functionServiceMapObj{meta: f})
}
