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
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	executorClient "github.com/fission/fission/pkg/executor/client"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/metrics"
	"github.com/fission/fission/pkg/utils/otel"
	"github.com/fission/fission/pkg/utils/tracing"
)

// HTTPTriggerSet represents an HTTP trigger set
type HTTPTriggerSet struct {
	*functionServiceMap
	*mutableRouter

	logger                     *zap.Logger
	fissionClient              versioned.Interface
	kubeClient                 kubernetes.Interface
	executor                   *executorClient.Client
	resolver                   *functionReferenceResolver
	triggers                   []fv1.HTTPTrigger
	triggerInformer            k8sCache.SharedIndexInformer
	functions                  []fv1.Function
	funcInformer               k8sCache.SharedIndexInformer
	updateRouterRequestChannel chan struct{}
	tsRoundTripperParams       *tsRoundTripperParams
	isDebugEnv                 bool
	svcAddrUpdateThrottler     *throttler.Throttler
	unTapServiceTimeout        time.Duration
}

<<<<<<< HEAD
func init() {
	_ = loadFeatureConfigmap()
}

func loadFeatureConfigmap() error {
	var err error
	featureConfig, err = config.GetFeatureConfig()
	if err != nil {
		fmt.Println(err)
		return errors.New("error while loading feature configmap")
	}
	return nil
}

func makeHTTPTriggerSet(logger *zap.Logger, fmap *functionServiceMap, fissionClient versioned.Interface,
	kubeClient kubernetes.Interface, executor *executorClient.Client, params *tsRoundTripperParams, isDebugEnv bool, unTapServiceTimeout time.Duration, actionThrottler *throttler.Throttler) *HTTPTriggerSet {
=======
func makeHTTPTriggerSet(logger *zap.Logger, fmap *functionServiceMap, fissionClient *crd.FissionClient,
	kubeClient *kubernetes.Clientset, executor *executorClient.Client, params *tsRoundTripperParams, isDebugEnv bool, unTapServiceTimeout time.Duration, actionThrottler *throttler.Throttler) *HTTPTriggerSet {
>>>>>>> de1888c1 (Removed featureConfig as global variable)

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
	}

	informerFactory := genInformer.NewSharedInformerFactory(fissionClient, time.Minute*30)
	httpTriggerSet.triggerInformer = informerFactory.Core().V1().HTTPTriggers().Informer()
	httpTriggerSet.funcInformer = informerFactory.Core().V1().Functions().Informer()

	httpTriggerSet.addTriggerHandlers()
	httpTriggerSet.addFunctionHandlers()
	return httpTriggerSet
}

func (ts *HTTPTriggerSet) subscribeRouter(ctx context.Context, mr *mutableRouter) {
	resolver := makeFunctionReferenceResolver(&ts.funcInformer)
	ts.resolver = resolver
	ts.mutableRouter = mr

	if ts.fissionClient == nil {
		// Used in tests only.
		mr.updateRouter(ts.getRouter(nil))
		ts.logger.Info("skipping continuous trigger updates")
		return
	}
	go ts.updateRouter()
	go ts.syncTriggers()
	go ts.runInformer(ctx, ts.funcInformer)
	go ts.runInformer(ctx, ts.triggerInformer)
}

func defaultHomeHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func routerHealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (ts *HTTPTriggerSet) getRouter(fnTimeoutMap map[types.UID]int) *mux.Router {

	featureConfig, _ := config.GetFeatureConfig()

	muxRouter := mux.NewRouter()
	muxRouter.Use(metrics.HTTPMetricMiddleware())
	if featureConfig.AuthConfig.IsEnabled {
		muxRouter.Use(authMiddleware(featureConfig))
	}

	openTracingEnabled := tracing.TracingEnabled(ts.logger)

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
			openTracingEnabled:       openTracingEnabled,
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

		methods := trigger.Spec.Methods
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

		var handler http.Handler
		if openTracingEnabled {
			handler = http.HandlerFunc(fh.handler)
		} else {
			if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
				handler = otel.GetHandlerWithOTEL(http.HandlerFunc(fh.handler), *trigger.Spec.Prefix)
			} else {
				handler = otel.GetHandlerWithOTEL(http.HandlerFunc(fh.handler), trigger.Spec.RelativeURL)
			}
		}

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

		var handler http.Handler
		internalRoute := utils.UrlForFunction(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
		internalPrefixRoute := internalRoute + "/"
		if openTracingEnabled {
			handler = http.HandlerFunc(fh.handler)
		} else {
			handler = otel.GetHandlerWithOTEL(http.HandlerFunc(fh.handler), internalRoute)
		}

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

	return muxRouter
}

func (ts *HTTPTriggerSet) updateTriggerStatusFailed(ht *fv1.HTTPTrigger, err error) {
	// TODO
}

func (ts *HTTPTriggerSet) addTriggerHandlers() {
	ts.triggerInformer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			trigger := obj.(*fv1.HTTPTrigger)
			go createIngress(ts.logger, trigger, ts.kubeClient)
			ts.syncTriggers()
		},
		DeleteFunc: func(obj interface{}) {
			ts.syncTriggers()
			trigger := obj.(*fv1.HTTPTrigger)
			go deleteIngress(ts.logger, trigger, ts.kubeClient)
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			oldTrigger := oldObj.(*fv1.HTTPTrigger)
			newTrigger := newObj.(*fv1.HTTPTrigger)

			if oldTrigger.ObjectMeta.ResourceVersion == newTrigger.ObjectMeta.ResourceVersion {
				return
			}

			go updateIngress(ts.logger, oldTrigger, newTrigger, ts.kubeClient)
			ts.syncTriggers()
		},
	})
}

func (ts *HTTPTriggerSet) addFunctionHandlers() {
	ts.funcInformer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
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
}

func (ts *HTTPTriggerSet) runInformer(ctx context.Context, informer k8sCache.SharedIndexInformer) {
	go func() {
		informer.Run(ctx.Done())
	}()
}

func (ts *HTTPTriggerSet) syncTriggers() {
	ts.updateRouterRequestChannel <- struct{}{}
}

func (ts *HTTPTriggerSet) updateRouter() {
	for range ts.updateRouterRequestChannel {
		// get triggers
		latestTriggers := ts.triggerInformer.GetStore().List()
		triggers := make([]fv1.HTTPTrigger, len(latestTriggers))
		for _, t := range latestTriggers {
			triggers = append(triggers, *t.(*fv1.HTTPTrigger))
		}
		ts.triggers = triggers

		// get functions
		latestFunctions := ts.funcInformer.GetStore().List()
		functionTimeout := make(map[types.UID]int, len(latestFunctions))
		functions := make([]fv1.Function, len(latestFunctions))
		for _, f := range latestFunctions {
			fn := *f.(*fv1.Function)
			functionTimeout[fn.ObjectMeta.UID] = fn.Spec.FunctionTimeout
			functions = append(functions, *f.(*fv1.Function))
		}
		ts.functions = functions

		// make a new router and use it
		ts.mutableRouter.updateRouter(ts.getRouter(functionTimeout))
	}
}
