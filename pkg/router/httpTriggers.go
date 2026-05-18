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
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bep/debounce"
	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	ferror "github.com/fission/fission/pkg/error"
	eclient "github.com/fission/fission/pkg/executor/client"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/info"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/metrics"
)

// HTTPTriggerSet represents an HTTP trigger set
type HTTPTriggerSet struct {
	*functionServiceMap
	*mutableRouter

	// internalMutableRouter is the mutable wrapper for the internal listener.
	// It is updated in lockstep with mutableRouter (the public listener) on
	// every trigger / function reconciliation. May be nil in unit-test paths
	// that only construct the public mux directly.
	internalMutableRouter *mutableRouter

	logger                     logr.Logger
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
	useEncodedPath             bool
	svcAddrUpdateThrottler     *throttler.Throttler
	unTapServiceTimeout        time.Duration
	syncDebouncer              func(func())
}

func makeHTTPTriggerSet(logger logr.Logger, fmap *functionServiceMap, fissionClient versioned.Interface,
	kubeClient kubernetes.Interface, executor eclient.ClientInterface, params *tsRoundTripperParams, isDebugEnv bool, useEncodedPath bool, unTapServiceTimeout time.Duration, actionThrottler *throttler.Throttler) (*HTTPTriggerSet, error) {

	httpTriggerSet := &HTTPTriggerSet{
		logger:                     logger.WithName("http_trigger_set"),
		functionServiceMap:         fmap,
		triggers:                   []fv1.HTTPTrigger{},
		fissionClient:              fissionClient,
		kubeClient:                 kubeClient,
		executor:                   executor,
		updateRouterRequestChannel: make(chan struct{}, 10), // use buffer channel
		tsRoundTripperParams:       params,
		isDebugEnv:                 isDebugEnv,
		useEncodedPath:             useEncodedPath,
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

// subscribeRouter wires the public mutable router and (optionally) the
// internal mutable router. Passing internalMR=nil preserves the legacy
// single-listener path used by older test scaffolding.
func (ts *HTTPTriggerSet) subscribeRouter(ctx context.Context, mgr manager.Interface, mr *mutableRouter, internalMR *mutableRouter) error {
	resolver := makeFunctionReferenceResolver(ts.logger, ts.funcInformer)
	ts.resolver = resolver
	ts.mutableRouter = mr
	ts.internalMutableRouter = internalMR

	if ts.fissionClient == nil {
		// Used in tests only.
		public, internal, err := ts.buildMuxes(nil)
		if err != nil {
			return err
		}
		mr.updateRouter(public)
		if internalMR != nil {
			internalMR.updateRouter(internal)
		}
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

// buildMuxes constructs the two mux.Router instances that back the router
// process: one for the public listener (user HTTPTriggers + healthz +
// version + optional auth) and one for the internal listener
// (`/fission-function/<ns>/<name>` and its prefix variant). Splitting
// the registrations is the core of GHSA-3g33-6vg6-27m8 — internal
// invocation routes must never be reachable from the public listener,
// because they bypass HTTPTrigger gates (auth middleware, host/method
// matching) by design.
//
// The internal mux deliberately omits the metrics middleware, the auth
// middleware, and the GKE-ingress `/` no-op handler: those concerns are
// public-listener only. Callers wrap the internal mux with
// hmac.Verifier in the bundle process; the verifier is intentionally
// not added here so this function stays unit-testable without HMAC env
// state.
func (ts *HTTPTriggerSet) buildMuxes(fnTimeoutMap map[types.UID]int) (public, internal *mux.Router, err error) {
	featureConfig, err := config.GetFeatureConfig(ts.logger)
	if err != nil {
		return nil, nil, err
	}

	public = mux.NewRouter()
	internal = mux.NewRouter()
	// Honour USE_ENCODED_PATH (see issue #1317) on every reconciliation:
	// buildMuxes is called repeatedly by updateRouter and the resulting
	// routers are atomically swapped under the listener handlers. If we
	// don't apply UseEncodedPath here, the feature works only until the
	// first reconciliation and then silently turns off.
	if ts.useEncodedPath {
		public = public.UseEncodedPath()
		internal = internal.UseEncodedPath()
	}

	public.Use(metrics.HTTPMetricMiddleware)
	if featureConfig.AuthConfig.IsEnabled {
		public.Use(authMiddleware(featureConfig))
	}

	// HTTP triggers setup by the user — public listener only.
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
			ts.logger.Error(nil, "resolve result type not implemented", "type", rr.resolveResultType)
			os.Exit(1)
		}

		fh := &functionHandler{
			logger:                   ts.logger.WithName(trigger.Name),
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

		methods := trigger.Spec.Methods
		if len(trigger.Spec.Method) > 0 {
			present := slices.Contains(trigger.Spec.Methods, trigger.Spec.Method)
			if !present {
				methods = append(methods, trigger.Spec.Method)
			}
		}

		handler := http.HandlerFunc(fh.handler)

		if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
			prefix := *trigger.Spec.Prefix
			if strings.HasSuffix(prefix, "/") {
				ht := public.PathPrefix(prefix).Handler(handler)
				ht.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht.Host(trigger.Spec.Host)
				}
				ts.logger.V(1).Info("add prefix route for function", "route", prefix, "function", fh.function, "methods", methods)
			} else {
				ht1 := public.Handle(prefix, handler)
				ht1.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht1.Host(trigger.Spec.Host)
				}
				ht2 := public.PathPrefix(prefix + "/").Handler(handler)
				ht2.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht2.Host(trigger.Spec.Host)
				}
				ts.logger.V(1).Info("add prefix and handler route for function", "route", prefix, "function", fh.function, "methods", methods)
			}
		} else {
			ht := public.Handle(trigger.Spec.RelativeURL, handler)
			ht.Methods(methods...)
			if trigger.Spec.Host != "" {
				ht.Host(trigger.Spec.Host)
			}
			ts.logger.V(1).Info("add handler route for function", "router", trigger.Spec.RelativeURL, "function", fh.function, "methods", methods)
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
		// Router-owned probe; never a CORS surface for legitimate
		// browser code, so reject cross-origin preflights and strip any
		// Access-Control-* header that might be added in the future.
		public.Handle("/", httpsecurity.DenyAllCORS(http.HandlerFunc(defaultHomeHandler))).Methods("GET")
	}

	// Internal triggers for each function by name. Non-http triggers
	// (timer, kubewatcher, mqtrigger) and the executor's invocation
	// path land here. These routes live ONLY on the internal mux —
	// exposing them on the public listener is GHSA-3g33-6vg6-27m8.
	for i := range ts.functions {
		fn := ts.functions[i]
		fh := &functionHandler{
			logger:                 ts.logger.WithName(fn.Name),
			fmap:                   ts.functionServiceMap,
			function:               &fn,
			executor:               ts.executor,
			tsRoundTripperParams:   ts.tsRoundTripperParams,
			isDebugEnv:             ts.isDebugEnv,
			svcAddrUpdateThrottler: ts.svcAddrUpdateThrottler,
			functionTimeoutMap:     fnTimeoutMap,
			unTapServiceTimeout:    ts.unTapServiceTimeout,
		}

		internalRoute := utils.UrlForFunction(fn.Name, fn.Namespace)
		internalPrefixRoute := internalRoute + "/"
		handler := http.HandlerFunc(fh.handler)
		internal.Handle(internalRoute, handler)
		internal.PathPrefix(internalPrefixRoute).Handler(handler)
		ts.logger.V(1).Info("add internal handler and prefix route for function", "router", internalRoute, "function", fn)
	}

	if featureConfig.AuthConfig.IsEnabled {

		path := featureConfig.AuthConfig.AuthUriPath
		// Auth endpoint for the router. Router-owned route; cross-origin
		// browser callers are not a legitimate use case, so reject
		// preflights and strip any stray Access-Control-* headers.
		public.Handle(path, httpsecurity.DenyAllCORS(http.HandlerFunc(authLoginHandler(featureConfig)))).Methods("POST")
	}

	// Healthz endpoint for the router. Stays on the public listener so
	// existing readiness/liveness probes and external monitors keep
	// working without HMAC credentials. Router-owned route; deny CORS.
	public.Handle("/router-healthz", httpsecurity.DenyAllCORS(http.HandlerFunc(routerHealthHandler))).Methods("GET")
	// version of application; router-owned route; deny CORS.
	public.Handle("/_version", httpsecurity.DenyAllCORS(http.HandlerFunc(versionHandler))).Methods("GET")

	return public, internal, nil
}

func (ts *HTTPTriggerSet) updateTriggerStatusFailed(ht *fv1.HTTPTrigger, err error) {
	// TODO
}

// markTriggerCondition writes RouteAdmitted + Ready conditions on an
// HTTPTrigger. status=True with reason=RouteAdmitted on a successful mux
// install; status=False with reason=MuxBuildFailed when buildMuxes errors
// out, so users polling conditions don't see a stale True after a failure.
//
// Fast-path: the trigger snapshot from the informer is checked against
// the desired condition; we only Get + UpdateStatus when this trigger
// would actually transition (matches the user's "key transitions only"
// expectation). updateRouter runs on every debounced trigger/function
// event, so the fast path is what keeps API traffic bounded.
func (ts *HTTPTriggerSet) markTriggerCondition(ctx context.Context, trigger *fv1.HTTPTrigger, status metav1.ConditionStatus, reason, admittedMessage, readyMessage string) {
	if ts.fissionClient == nil {
		return // unit-test wiring without a real client
	}
	// Truncate to the apiserver's 32KB Condition.message cap; the failure
	// path embeds err.Error() which is otherwise unbounded.
	admittedMessage = conditions.TruncateMessage(admittedMessage)
	readyMessage = conditions.TruncateMessage(readyMessage)
	wantAdmitted := metav1.Condition{
		Type: fv1.HTTPTriggerConditionRouteAdmitted, Status: status,
		ObservedGeneration: trigger.Generation, Reason: reason, Message: admittedMessage,
	}
	wantReady := metav1.Condition{
		Type: fv1.HTTPTriggerConditionReady, Status: status,
		ObservedGeneration: trigger.Generation, Reason: reason, Message: readyMessage,
	}
	if conditions.IsAt(trigger.Status.Conditions, wantAdmitted) &&
		conditions.IsAt(trigger.Status.Conditions, wantReady) {
		return
	}
	cur, err := ts.fissionClient.CoreV1().HTTPTriggers(trigger.Namespace).Get(ctx, trigger.Name, metav1.GetOptions{})
	if err != nil {
		ts.logger.V(1).Info("httptrigger status: get failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
		return
	}
	wantAdmitted.ObservedGeneration = cur.Generation
	wantReady.ObservedGeneration = cur.Generation
	if conditions.IsAt(cur.Status.Conditions, wantAdmitted) &&
		conditions.IsAt(cur.Status.Conditions, wantReady) {
		return
	}
	conditions.Set(&cur.Status.Conditions, wantAdmitted)
	conditions.Set(&cur.Status.Conditions, wantReady)
	if _, err := ts.fissionClient.CoreV1().HTTPTriggers(trigger.Namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		ts.logger.V(1).Info("httptrigger status: update failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
	}
}

func (ts *HTTPTriggerSet) addTriggerHandlers() error {
	for _, triggerInformer := range ts.triggerInformer {
		_, err := triggerInformer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				trigger := obj.(*fv1.HTTPTrigger)
				go createIngress(context.Background(), ts.logger, trigger, ts.kubeClient)
				ts.syncTriggers()
			},
			DeleteFunc: func(obj any) {
				ts.syncTriggers()
				trigger := obj.(*fv1.HTTPTrigger)
				go deleteIngress(context.Background(), ts.logger, trigger, ts.kubeClient)
			},
			UpdateFunc: func(oldObj any, newObj any) {
				oldTrigger := oldObj.(*fv1.HTTPTrigger)
				newTrigger := newObj.(*fv1.HTTPTrigger)

				// Skip status-only updates: our own markTriggerCondition
				// writes bump RV but leave Generation untouched. If we
				// re-synced on every status-only update we'd loop on our
				// own writes (mux rebuild → status write → informer
				// fires → mux rebuild).
				if oldTrigger.Generation == newTrigger.Generation {
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
			AddFunc: func(obj any) {
				ts.syncTriggers()
			},
			DeleteFunc: func(obj any) {
				ts.syncTriggers()
			},
			UpdateFunc: func(oldObj any, newObj any) {
				oldFn := oldObj.(*fv1.Function)
				fn := newObj.(*fv1.Function)

				// Generation-based comparison filters out status-only
				// updates (executor's FunctionConditionReady writes go
				// through the status subresource and don't bump
				// Generation). Without this, every cold-start specialization
				// would invalidate the resolver cache and force a full
				// mux rebuild on the next request.
				if oldFn.Generation == fn.Generation {
					return
				}

				// update resolver function reference cache
				for key, rr := range ts.resolver.copy() {
					if key.namespace == fn.Namespace &&
						rr.functionMap[fn.Name] != nil &&
						rr.functionMap[fn.Name].ResourceVersion != fn.ResourceVersion {
						// invalidate resolver cache
						ts.logger.V(1).Info("invalidating resolver cache")
						ts.resolver.delete(key.namespace, key.triggerName, key.triggerResourceVersion)
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
				functionTimeout[fn.UID] = fn.Spec.FunctionTimeout
				allfunctions = append(allfunctions, fn)
			}
		}
		ts.functions = allfunctions

		// Rebuild both muxes from the same function snapshot then swap
		// each in turn. The two updateRouter calls are sequential, not
		// transactional — a request that arrives between the two swaps
		// could see an updated public mux and a stale internal mux for
		// a few microseconds, but both muxes derive from the same
		// `functions` snapshot so the function set served by either
		// listener is consistent in steady state. True atomicity across
		// listeners would require a shared atomic.Pointer holding both
		// muxes; not worth the complexity for this case.
		public, internal, err := ts.buildMuxes(functionTimeout)
		if err != nil {
			ts.logger.Error(err, "error updating router")
			// Flip every trigger to Ready=False so consumers polling
			// conditions don't see a stale True after a failed resync.
			// Fast-path inside markTriggerCondition skips no-op writes.
			for i := range alltriggers {
				ts.markTriggerCondition(ctx, &alltriggers[i],
					metav1.ConditionFalse, fv1.HTTPTriggerReasonMuxBuildFail,
					"router failed to build mux: "+err.Error(),
					"trigger is not serving due to router mux error")
			}
			continue
		}
		ts.mutableRouter.updateRouter(public)
		if ts.internalMutableRouter != nil {
			ts.internalMutableRouter.updateRouter(internal)
		}

		// Mark each trigger admitted now that its routes are live. We
		// do this after the swap so the condition reflects observable
		// state, not just intent. Best-effort; never blocks subsequent
		// mux updates.
		for i := range alltriggers {
			ts.markTriggerCondition(ctx, &alltriggers[i],
				metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted,
				"router accepted the trigger and installed its mux entry",
				"trigger is serving")
		}
	}
}
