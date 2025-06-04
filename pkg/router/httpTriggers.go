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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bep/debounce"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	eclient "github.com/fission/fission/pkg/executor/client"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/info"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/metrics"
)

// HTTPTriggerSet represents an HTTP trigger set
type HTTPTriggerSet struct {
	*functionServiceMap
	*mutableRouter

	logger                     *zap.Logger
	fissionClient              versioned.Interface
	kubeClient                 kubernetes.Interface
	executor                   eclient.ClientInterface
	resolver                   *functionReferenceResolver
	triggers                   []fv1.HTTPTrigger
	triggerInformer            map[string]k8sCache.SharedIndexInformer
	functions                  []fv1.Function
	funcInformer               map[string]k8sCache.SharedIndexInformer
	updateRouterRequestChannel chan struct{}
	tsRoundTripperParams       *tsRoundTripperParams
	isDebugEnv                 bool
	svcAddrUpdateThrottler     *throttler.Throttler
	unTapServiceTimeout        time.Duration
	syncDebouncer              func(func())
}

// contextKey is a type for context keys
type contextKey string

const httpTriggerSetKey contextKey = "httpTriggerSet"

// withHTTPTriggerSet is a middleware that adds the HTTPTriggerSet to the request context
func withHTTPTriggerSet(ts *HTTPTriggerSet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), httpTriggerSetKey, ts)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func makeHTTPTriggerSet(logger *zap.Logger, fmap *functionServiceMap, fissionClient versioned.Interface,
	kubeClient kubernetes.Interface, executor eclient.ClientInterface, params *tsRoundTripperParams, isDebugEnv bool, unTapServiceTimeout time.Duration, actionThrottler *throttler.Throttler) (*HTTPTriggerSet, error) {

	httpTriggerSet := &HTTPTriggerSet{
		logger:                     logger.Named("http_trigger_set"),
		functionServiceMap:         fmap,
		triggers:                   []fv1.HTTPTrigger{},
		fissionClient:              fissionClient,
		kubeClient:                 kubeClient,
		executor:                   executor,
		updateRouterRequestChannel: make(chan struct{}, 10), // use buffer channel
		tsRoundTripperParams:       params,
		isDebugEnv:                 isDebugEnv,
		svcAddrUpdateThrottler:     actionThrottler,
		unTapServiceTimeout:        unTapServiceTimeout,
		syncDebouncer:              debounce.New(time.Millisecond * 20),
	}
	httpTriggerSet.triggerInformer = utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.HttpTriggerResource)
	httpTriggerSet.funcInformer = utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.FunctionResource)
	err := httpTriggerSet.addTriggerHandlers()
	if err != nil {
		return nil, err
	}
	err = httpTriggerSet.addFunctionHandlers()
	if err != nil {
		return nil, err
	}
	return httpTriggerSet, nil
}

func (ts *HTTPTriggerSet) subscribeRouter(ctx context.Context, mgr manager.Interface, mr *mutableRouter) error {
	resolver := makeFunctionReferenceResolver(ts.logger, ts.funcInformer)
	ts.resolver = resolver
	ts.mutableRouter = mr

	if ts.fissionClient == nil {
		// Used in tests only.
		router, err := ts.getRouter(nil)
		if err != nil {
			return err
		}
		mr.updateRouter(router)
		ts.logger.Info("skipping continuous trigger updates")
		return nil
	}
	mgr.Add(ctx, func(ctx context.Context) {
		ts.updateRouter(ctx)
	})
	ts.syncTriggers()
	mgr.AddInformers(ctx, ts.funcInformer)
	mgr.AddInformers(ctx, ts.triggerInformer)
	return nil
}

func defaultHomeHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func routerHealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write([]byte(info.ApiInfo().String()))
	if err != nil {
		se, ok := err.(*kerrors.StatusError)
		if ok {
			http.Error(w, se.Error(), int(se.ErrStatus.Code))
			return
		}

		code, msg := ferror.GetHTTPError(err)
		http.Error(w, msg, code)
	}
}

func openAPIHandler(w http.ResponseWriter, r *http.Request) {
	ts, ok := r.Context().Value(httpTriggerSetKey).(*HTTPTriggerSet)
	if !ok {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	type OpenAPISpec struct {
		OpenAPI    string                       `json:"openapi"`
		Info       map[string]interface{}       `json:"info"`
		Paths      map[string]openapi3.PathItem `json:"paths"`
		Components struct {
			Schemas map[string]openapi3.Schema `json:"schemas"`
		} `json:"components"`
	}

	spec := OpenAPISpec{
		OpenAPI: "3.0.0",
		Info: map[string]interface{}{
			"title":       "Fission HTTP Triggers",
			"description": "Auto-generated OpenAPI spec for Fission HTTP Triggers",
			"version":     "1.0.0",
		},
		Paths: map[string]openapi3.PathItem{},
		Components: struct {
			Schemas map[string]openapi3.Schema `json:"schemas"`
		}{
			Schemas: map[string]openapi3.Schema{},
		},
	}

	for _, trigger := range ts.triggers {
		if trigger.Spec.OpenAPISpec != nil {
			spec.Paths[trigger.Spec.RelativeURL] = trigger.Spec.OpenAPISpec.PathItem

			for name, schema := range trigger.Spec.OpenAPISpec.Schemas {
				spec.Components.Schemas[name] = schema
			}
		} else {
			methods := trigger.Spec.Methods
			if len(methods) == 0 && trigger.Spec.Method != "" {
				methods = []string{trigger.Spec.Method}
			}
			if len(methods) == 0 {
				methods = []string{"POST"}
			}
			item := openapi3.PathItem{
				Summary:     fmt.Sprintf("Trigger: %s", trigger.ObjectMeta.Name),
				Description: fmt.Sprintf("Function: %s", trigger.Spec.FunctionReference.Name),
			}
			for _, m := range methods {
				verb := strings.ToLower(m)
				switch verb {
				case "get":
					item.Get = openapi3.NewOperation()
					item.Get.Responses = openapi3.NewResponses(
						openapi3.WithName("200", openapi3.NewResponse().WithDescription("Successful response")),
					)
				case "post":
					item.Post = openapi3.NewOperation()
					item.Post.Responses = openapi3.NewResponses(
						openapi3.WithName("200", openapi3.NewResponse().WithDescription("Successful response")),
					)
				case "put":
					item.Put = openapi3.NewOperation()
					item.Put.Responses = openapi3.NewResponses(
						openapi3.WithName("200", openapi3.NewResponse().WithDescription("Successful response")),
					)
				case "delete":
					item.Delete = openapi3.NewOperation()
					item.Delete.Responses = openapi3.NewResponses(
						openapi3.WithName("200", openapi3.NewResponse().WithDescription("Successful response")),
					)
				}
			}
			spec.Paths[trigger.Spec.RelativeURL] = item
		}
	}

	jsonData, err := json.Marshal(spec)
	if err != nil {
		http.Error(w, "Error generating OpenAPI spec", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(jsonData)
	if err != nil {
		http.Error(w, "Error writing response", http.StatusInternalServerError)
		return
	}
}

func (ts *HTTPTriggerSet) getRouter(fnTimeoutMap map[types.UID]int) (*mux.Router, error) {

	featureConfig, err := config.GetFeatureConfig(ts.logger)
	if err != nil {
		return nil, err
	}

	muxRouter := mux.NewRouter()
	muxRouter.Use(metrics.HTTPMetricMiddleware)
	if featureConfig.AuthConfig.IsEnabled {
		muxRouter.Use(authMiddleware(featureConfig))
	}

	// Add the OpenAPI endpoint
	muxRouter.Handle("/v2/openapi", withHTTPTriggerSet(ts)(http.HandlerFunc(openAPIHandler))).Methods("GET")

	// HTTP triggers setup by the user
	homeHandled := false
	for i := range ts.triggers {
		trigger := ts.triggers[i]

		// resolve function reference
		rr, err := ts.resolver.resolve(trigger)
		if err != nil {
			// Unresolvable function reference. Report the error via
			// the trigger's status.
			go ts.updateTriggerStatusFailed(&trigger, err)

			// Ignore this route and let it 404.
			continue
		}

		if rr.resolveResultType != resolveResultSingleFunction && rr.resolveResultType != resolveResultMultipleFunctions {
			// not implemented yet
			ts.logger.Panic("resolve result type not implemented", zap.Any("type", rr.resolveResultType))
		}

		fh := &functionHandler{
			logger:                   ts.logger.Named(trigger.ObjectMeta.Name),
			fmap:                     ts.functionServiceMap,
			executor:                 ts.executor,
			httpTrigger:              &trigger,
			functionMap:              rr.functionMap,
			fnWeightDistributionList: rr.functionWtDistributionList,
			tsRoundTripperParams:     ts.tsRoundTripperParams,
			isDebugEnv:               ts.isDebugEnv,
			svcAddrUpdateThrottler:   ts.svcAddrUpdateThrottler,
			functionTimeoutMap:       fnTimeoutMap,
			unTapServiceTimeout:      ts.unTapServiceTimeout,
		}

		// The functionHandler for HTTP trigger with fn reference type "FunctionReferenceTypeFunctionName",
		// it's function metadata is set here.

		// The functionHandler For HTTP trigger with fn reference type "FunctionReferenceTypeFunctionWeights",
		// it's function metadata is decided dynamically before proxying the request in order to support canary
		// deployment. For more details, please check "handler" function of functionHandler.

		if rr.resolveResultType == resolveResultSingleFunction {
			for _, fn := range fh.functionMap {
				fh.function = fn
			}
		}

		// Get methods from OpenAPISpec if available, otherwise use basic configuration
		var methods []string
		if trigger.Spec.OpenAPISpec != nil {
			if trigger.Spec.OpenAPISpec.Connect != nil {
				methods = append(methods, "CONNECT")
			}
			if trigger.Spec.OpenAPISpec.Delete != nil {
				methods = append(methods, "DELETE")
			}
			if trigger.Spec.OpenAPISpec.Get != nil {
				methods = append(methods, "GET")
			}
			if trigger.Spec.OpenAPISpec.Head != nil {
				methods = append(methods, "HEAD")
			}
			if trigger.Spec.OpenAPISpec.Options != nil {
				methods = append(methods, "OPTIONS")
			}
			if trigger.Spec.OpenAPISpec.Patch != nil {
				methods = append(methods, "PATCH")
			}
			if trigger.Spec.OpenAPISpec.Post != nil {
				methods = append(methods, "POST")
			}
			if trigger.Spec.OpenAPISpec.Put != nil {
				methods = append(methods, "PUT")
			}
			if trigger.Spec.OpenAPISpec.Trace != nil {
				methods = append(methods, "TRACE")
			}

			if len(methods) == 0 {
				methods = []string{"POST"}
			}
		}

		// Fall back to basic method configuration if no methods found in OpenAPISpec
		if len(methods) == 0 {
			methods = trigger.Spec.Methods
			if len(trigger.Spec.Method) > 0 {
				present := false
				for _, m := range trigger.Spec.Methods {
					if m == trigger.Spec.Method {
						present = true
						break
					}
				}
				if !present {
					methods = append(methods, trigger.Spec.Method)
				}
			}
			if len(methods) == 0 {
				methods = []string{"POST"} // default fallback
			}
		}

		handler := http.HandlerFunc(fh.handler)

		if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
			prefix := *trigger.Spec.Prefix
			if strings.HasSuffix(prefix, "/") {
				ht := muxRouter.PathPrefix(prefix).Handler(handler)
				ht.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht.Host(trigger.Spec.Host)
				}
				ts.logger.Debug("add prefix route for function", zap.String("route", prefix), zap.Any("function", fh.function), zap.Strings("methods", methods))
			} else {
				ht1 := muxRouter.Handle(prefix, handler)
				ht1.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht1.Host(trigger.Spec.Host)
				}
				ht2 := muxRouter.PathPrefix(prefix + "/").Handler(handler)
				ht2.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht2.Host(trigger.Spec.Host)
				}
				ts.logger.Debug("add prefix and handler route for function", zap.String("route", prefix), zap.Any("function", fh.function), zap.Strings("methods", methods))
			}
		} else {
			ht := muxRouter.Handle(trigger.Spec.RelativeURL, handler)
			ht.Methods(methods...)
			if trigger.Spec.Host != "" {
				ht.Host(trigger.Spec.Host)
			}
			ts.logger.Debug("add handler route for function", zap.String("router", trigger.Spec.RelativeURL), zap.Any("function", fh.function), zap.Strings("methods", methods))
		}

		if trigger.Spec.Prefix == nil && trigger.Spec.RelativeURL == "/" && len(methods) == 1 && methods[0] == http.MethodGet {
			homeHandled = true
		}
	}
	if !homeHandled {
		//
		// This adds a no-op handler that returns 200-OK to make sure that the
		// "GET /" request succeeds.  This route is used by GKE Ingress (and
		// perhaps other ingress implementations) as a health check, so we don't
		// want it to be a 404 even if the user doesn't have a function mapped to
		// this route.
		//
		muxRouter.HandleFunc("/", defaultHomeHandler).Methods("GET")
	}

	// Internal triggers for each function by name. Non-http
	// triggers route into these.
	for i := range ts.functions {
		fn := ts.functions[i]
		fh := &functionHandler{
			logger:                 ts.logger.Named(fn.ObjectMeta.Name),
			fmap:                   ts.functionServiceMap,
			function:               &fn,
			executor:               ts.executor,
			tsRoundTripperParams:   ts.tsRoundTripperParams,
			isDebugEnv:             ts.isDebugEnv,
			svcAddrUpdateThrottler: ts.svcAddrUpdateThrottler,
			functionTimeoutMap:     fnTimeoutMap,
			unTapServiceTimeout:    ts.unTapServiceTimeout,
		}

		internalRoute := utils.UrlForFunction(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
		internalPrefixRoute := internalRoute + "/"
		handler := http.HandlerFunc(fh.handler)
		muxRouter.Handle(internalRoute, handler)
		muxRouter.PathPrefix(internalPrefixRoute).Handler(handler)
		ts.logger.Debug("add internal handler and prefix route for function", zap.String("router", internalRoute), zap.Any("function", fn))
	}

	if featureConfig.AuthConfig.IsEnabled {

		path := featureConfig.AuthConfig.AuthUriPath
		// Auth endpoint for the router.
		muxRouter.HandleFunc(path, authLoginHandler(featureConfig)).Methods("POST")
	}

	// Healthz endpoint for the router.
	muxRouter.HandleFunc("/router-healthz", routerHealthHandler).Methods("GET")
	// version of application.
	muxRouter.HandleFunc("/_version", versionHandler).Methods("GET")

	return muxRouter, nil
}

func (ts *HTTPTriggerSet) updateTriggerStatusFailed(ht *fv1.HTTPTrigger, err error) {
	// TODO
}

func (ts *HTTPTriggerSet) addTriggerHandlers() error {
	for _, triggerInformer := range ts.triggerInformer {
		_, err := triggerInformer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				trigger := obj.(*fv1.HTTPTrigger)
				go createIngress(context.Background(), ts.logger, trigger, ts.kubeClient)
				ts.syncTriggers()
			},
			DeleteFunc: func(obj interface{}) {
				ts.syncTriggers()
				trigger := obj.(*fv1.HTTPTrigger)
				go deleteIngress(context.Background(), ts.logger, trigger, ts.kubeClient)
			},
			UpdateFunc: func(oldObj interface{}, newObj interface{}) {
				oldTrigger := oldObj.(*fv1.HTTPTrigger)
				newTrigger := newObj.(*fv1.HTTPTrigger)

				if oldTrigger.ObjectMeta.ResourceVersion == newTrigger.ObjectMeta.ResourceVersion {
					return
				}

				go updateIngress(context.Background(), ts.logger, oldTrigger, newTrigger, ts.kubeClient)
				ts.syncTriggers()
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (ts *HTTPTriggerSet) addFunctionHandlers() error {
	for _, funcInformer := range ts.funcInformer {

		_, err := funcInformer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				ts.syncTriggers()
			},
			DeleteFunc: func(obj interface{}) {
				ts.syncTriggers()
			},
			UpdateFunc: func(oldObj interface{}, newObj interface{}) {
				oldFn := oldObj.(*fv1.Function)
				fn := newObj.(*fv1.Function)

				if oldFn.ObjectMeta.ResourceVersion == fn.ObjectMeta.ResourceVersion {
					return
				}

				// update resolver function reference cache
				for key, rr := range ts.resolver.copy() {
					if key.namespace == fn.ObjectMeta.Namespace &&
						rr.functionMap[fn.ObjectMeta.Name] != nil &&
						rr.functionMap[fn.ObjectMeta.Name].ObjectMeta.ResourceVersion != fn.ObjectMeta.ResourceVersion {
						// invalidate resolver cache
						ts.logger.Debug("invalidating resolver cache")
						err := ts.resolver.delete(key.namespace, key.triggerName, key.triggerResourceVersion)
						if err != nil {
							ts.logger.Error("error deleting functionReferenceResolver cache", zap.Error(err))
						}
						break
					}
				}
				ts.syncTriggers()
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (ts *HTTPTriggerSet) syncTriggers() {
	ts.syncDebouncer(func() {
		ts.updateRouterRequestChannel <- struct{}{}
	})
}

func (ts *HTTPTriggerSet) updateRouter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ts.updateRouterRequestChannel:
		}
		// get triggers
		alltriggers := make([]fv1.HTTPTrigger, 0)
		for _, triggerInformer := range ts.triggerInformer {
			latestTriggers := triggerInformer.GetStore().List()
			for _, t := range latestTriggers {
				alltriggers = append(alltriggers, *t.(*fv1.HTTPTrigger))
			}
		}
		ts.triggers = alltriggers

		// get functions
		allfunctions := make([]fv1.Function, 0)
		functionTimeout := make(map[types.UID]int, 0)
		for _, funcInformer := range ts.funcInformer {
			latestFunctions := funcInformer.GetStore().List()
			for _, f := range latestFunctions {
				fn := *f.(*fv1.Function)
				functionTimeout[fn.ObjectMeta.UID] = fn.Spec.FunctionTimeout
				allfunctions = append(allfunctions, fn)
			}
		}
		ts.functions = allfunctions

		// make a new router and use it
		router, err := ts.getRouter(functionTimeout)
		if err != nil {
			ts.logger.Error("error updating router", zap.Error(err))
			continue
		}
		ts.mutableRouter.updateRouter(router)
	}
}
