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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/cache"
	"github.com/prometheus/client_golang/prometheus"
)

type (
	functionMetricsMap struct {
		cache *cache.Cache // map[metadataKey]*functionMetrics
	}

	functionMetrics struct {
		requestCount prometheus.Counter
		latencyOverhead prometheus.Histogram
		functionErrorCount prometheus.Counter
	}
)

func makeFunctionMetricsMap(expiry time.Duration) *functionMetricsMap {
	return &functionMetricsMap{
		cache: cache.MakeCache(expiry, 0),
	}
}

func (fmap *functionMetricsMap) lookup(f *metav1.ObjectMeta) (*functionMetrics, error) {
	mk := keyFromMetadata(f)
	item, err := fmap.cache.Get(*mk)
	if err != nil {
		return nil, err
	}
	u := item.(*functionMetrics)
	return u, nil
}

func (fmap *functionMetricsMap) assign(f *metav1.ObjectMeta, fMetrics *functionMetrics) {
	mk := keyFromMetadata(f)
	err, old := fmap.cache.Set(*mk, fMetrics)
	if err != nil {
		if *fMetrics == *(old.(*functionMetrics)) {
			return
		}
		log.Printf("error caching function metrics for function with a different value: %v", err)
		// ignore error
	}
}
