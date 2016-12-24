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
	"log"
	"net/url"
	"time"

	"github.com/fission/fission"
	"github.com/fission/fission/cache"
)

type functionServiceMap struct {
	cache *cache.Cache // map[fission.Metadata]*url.URL
}

func makeFunctionServiceMap(expiry time.Duration) *functionServiceMap {
	return &functionServiceMap{
		cache: cache.MakeCache(expiry, 0),
	}
}

func (fmap *functionServiceMap) lookup(f *fission.Metadata) (*url.URL, error) {
	item, err := fmap.cache.Get(*f)
	if err != nil {
		return nil, err
	}
	u := item.(*url.URL)
	return u, nil
}

func (fmap *functionServiceMap) assign(f *fission.Metadata, serviceUrl *url.URL) {
	err, _ := fmap.cache.Set(*f, serviceUrl)
	if err != nil {
		log.Printf("error caching service url for function: %v", err)
		// ignore error
	}
}
