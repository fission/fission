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
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/throttler"
)

func TestRouter(t *testing.T) {
	// metadata for a fake function
	fnMeta := metav1.ObjectMeta{Name: "foo", Namespace: metav1.NamespaceDefault}

	// and a reference to it
	fr := fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName,
		Name: fnMeta.Name,
	}

	// start a fake service
	testResponseString := "hi"
	testServiceUrl := createBackendService(testResponseString)

	logger, err := zap.NewDevelopment()
	panicIf(err)

	// set up the cache with this fake service
	fmap := makeFunctionServiceMap(logger, 0)
	fmap.assign(&fnMeta, testServiceUrl)

	// HTTP trigger set with a trigger for this function
	triggers, _, _ := makeHTTPTriggerSet(logger, fmap, nil, nil, nil, nil,
		&tsRoundTripperParams{
			timeout:         50 * time.Millisecond,
			timeoutExponent: 2,
			maxRetries:      10,
		}, false, throttler.MakeThrottler(30*time.Second))
	triggerUrl := "/foo"
	triggers.triggers = append(triggers.triggers,
		fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "xxx",
				Namespace:       metav1.NamespaceDefault,
				ResourceVersion: "1234",
			},
			Spec: fv1.HTTPTriggerSpec{
				RelativeURL:       triggerUrl,
				FunctionReference: fr,
				Method:            "GET",
			},
		})

	// set up the resolver's cache for this function
	frr := makeFunctionReferenceResolver(nil)
	nfr := namespacedTriggerReference{
		namespace:              metav1.NamespaceDefault,
		triggerName:            "xxx",
		triggerResourceVersion: "1234",
	}

	fnMetaMap := make(map[string]*fv1.Function, 1)
	fnMetaMap[fnMeta.Name] = &fv1.Function{
		ObjectMeta: fnMeta,
	}

	rr := resolveResult{
		resolveResultType: resolveResultSingleFunction,
		functionMap:       fnMetaMap,
	}
	frr.refCache.Set(nfr, rr)

	// run the router
	port := 4242
	tracingSamplingRate := .5
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serve(ctx, logger, port, tracingSamplingRate, triggers, frr, false)
	time.Sleep(100 * time.Millisecond)

	// hit the router
	testUrl := fmt.Sprintf("http://localhost:%v%v", port, triggerUrl)
	testRequest(testUrl, testResponseString)
}
