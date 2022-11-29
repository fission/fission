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

package canaryconfigmgr

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cache "k8s.io/client-go/tools/cache"
)

type (
	canaryConfigCancelFuncMap struct {
		cache cache.ThreadSafeStore // map[metadataKey]*context.Context
	}

	CanaryProcessingInfo struct {
		CancelFunc *context.CancelFunc
		Ticker     *time.Ticker
	}
)

func makecanaryConfigCancelFuncMap() *canaryConfigCancelFuncMap {
	return &canaryConfigCancelFuncMap{
		cache: cache.NewThreadSafeStore(cache.Indexers{}, cache.Indices{}),
	}
}

func (cancelFuncMap *canaryConfigCancelFuncMap) lookup(f metav1.ObjectMeta) (*CanaryProcessingInfo, error) {
	mk, err := cache.MetaNamespaceKeyFunc(f)
	if err != nil {
		return nil, err
	}
	item, exists := cancelFuncMap.cache.Get(mk)
	if !exists {
		return nil, fmt.Errorf("error looking up canaryConfig %s", mk)
	}
	value := item.(*CanaryProcessingInfo)
	return value, nil
}

func (cancelFuncMap *canaryConfigCancelFuncMap) assign(f metav1.ObjectMeta, value *CanaryProcessingInfo) error {
	mk, err := cache.MetaNamespaceKeyFunc(f)
	if err != nil {
		return err
	}
	_, exists := cancelFuncMap.cache.Get(mk)
	if exists {
		return fmt.Errorf("error assigning canaryConfig %s", mk)
	}
	cancelFuncMap.cache.Add(mk, value)
	return nil
}

func (cancelFuncMap *canaryConfigCancelFuncMap) remove(f metav1.ObjectMeta) error {
	mk, err := cache.MetaNamespaceKeyFunc(f)
	if err != nil {
		return err
	}
	cancelFuncMap.cache.Delete(mk)
	return nil
}
