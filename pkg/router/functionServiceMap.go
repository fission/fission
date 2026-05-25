// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/url"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/cache"
)

type (
	functionServiceMap struct {
		logger logr.Logger
		cache  *cache.Cache[metadataKey, *url.URL]
	}

	// metav1.ObjectMeta is not hashable, so we make a hashable copy
	// of the subset of its fields that are identifiable.
	metadataKey struct {
		Name            string
		Namespace       string
		ResourceVersion string
	}
)

func makeFunctionServiceMap(logger logr.Logger, expiry time.Duration) *functionServiceMap {
	return &functionServiceMap{
		logger: logger.WithName("function_service_map"),
		cache:  cache.MakeCache[metadataKey, *url.URL](expiry, 0),
	}
}

func keyFromMetadata(m *metav1.ObjectMeta) *metadataKey {
	return &metadataKey{
		Name:            m.Name,
		Namespace:       m.Namespace,
		ResourceVersion: m.ResourceVersion,
	}
}

func (fmap *functionServiceMap) lookup(f *metav1.ObjectMeta) (*url.URL, error) {
	mk := keyFromMetadata(f)
	item, err := fmap.cache.Get(*mk)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (fmap *functionServiceMap) assign(f *metav1.ObjectMeta, serviceURL *url.URL) {
	mk := keyFromMetadata(f)
	fmap.cache.Upsert(*mk, serviceURL)
}

func (fmap *functionServiceMap) remove(f *metav1.ObjectMeta) {
	mk := keyFromMetadata(f)
	fmap.cache.Delete(*mk)
}
