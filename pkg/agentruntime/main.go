// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package agentruntime implements the fission-bundle --agentPort subsystem:
// session dispatch, a per-replica registry of agent-enabled Functions, and
// an idle/archive sweeper for their sessions. It watches Function CRDs
// (Spec.Agent != nil), resolves an inbound "/agents/{namespace}/{name}" turn
// to the matching Function, forwards it to the router internal listener with
// the same ServiceRouterInternal HMAC signing the other publishers use, and
// meters the result into a per-session record. In v1, only Content-Type, the
// session header, and the runtime's own turn/wake headers (X-Fission-Agent-
// Turns, and X-Fission-Agent-Wake-Id/-Attempt on wake-delivered turns) are
// forwarded upstream, and only Content-Type plus X-Fission-Agent-Yield are
// returned; custom caller headers are intentionally not proxied in v1
// (revisit with the SDK slice).
//
// Sessions are statestore records, never CRDs — there is no AgentSession CRD
// and no reconcile loop over session state. The sweeper that ages sessions
// active -> idle -> archived runs on every replica with no leader election:
// every transition is a CAS write at the version the sweeper (or the request
// dispatcher) just read, so two replicas racing the same record simply have
// one CAS win and one CAS lose silently — that idempotence is the substitute
// for leader election, the same way the router-admitted request path never
// needed one.
//
// Known follow-ups (not in v1): no status condition is written back to the
// Function (unlike mcp's ToolExposed) — once the agent runtime has an
// observable signal worth surfacing, add an AgentReady/AgentExposed
// condition the same way pkg/mcp/reconciler.go's controller.SetConditions
// calls do. authz.go is a hand-mirrored duplicate of pkg/mcp/authz.go rather
// than a shared package; extracting the common bearer-token verifier into
// one place both subsystems import is deferred cleanup, not a design choice
// worth blocking this slice on.
package agentruntime

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/fission/fission/pkg/agentruntime/ui"
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/client"   // embedded-mode driver
	_ "github.com/fission/fission/pkg/statestore/memory"   // dev/test driver
	_ "github.com/fission/fission/pkg/statestore/postgres" // external-mode driver
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/httpx"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// agentruntimeScheme is the agent runtime Manager's scheme: the Fission CRD
// types plus the Kubernetes built-ins (EndpointSlice + Pod, for the pool
// introspection index). Built exactly like routerScheme
// (pkg/router/router.go:87-91) and used UNCONDITIONALLY in the manager
// Options, never gated on a pool-introspection flag: the generated clientset
// scheme (scheme.Scheme) alone has no EndpointSlice/Pod kinds registered, so
// GetInformer(ctx, &discoveryv1.EndpointSlice{}) errors before the RBAC
// preflight even runs.
var agentruntimeScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(agentruntimeScheme))
	utilruntime.Must(scheme.AddToScheme(agentruntimeScheme))
}

// poolWatchNamespaces returns the namespaces the pool introspection cache
// (EndpointSlice + Pod) watches: the function namespaces, where the
// executor's per-function Services and pool/specialized pods live. Mirrors
// router.go's sliceWatchNamespaces exactly.
func poolWatchNamespaces() []string {
	return utils.DefaultNSResolver().FunctionNamespaces()
}

// poolCacheOptions extends base with ByObject entries for EndpointSlice and
// Pod, called only after checkPoolRBAC has succeeded (see Start) — wiring
// these unconditionally and letting a forbidden LIST wedge the manager cache
// sync is exactly the crash-loop this feature must never cause.
//
// EndpointSlice is filtered on fv1.MANAGED_BY_LABEL, mirroring the router's
// own slice watch (router.go:111-113) — the EndpointSlice controller mirrors
// that label from the owning Service.
//
// Pod is filtered on fv1.EXECUTOR_TYPE being one of the three known executor
// types, NOT fv1.MANAGED_BY_LABEL: MANAGED_BY_LABEL is set on Fission-owned
// Services only (gp_service.go, newdeploy.go, container/svc.go) and mirrored
// onto EndpointSlices — it is NEVER set on a Pod, warm or specialized, in any
// of the three executor types. A literal MANAGED_BY_LABEL filter on Pod
// matches zero objects in production, silently emptying the pool panel every
// install. EXECUTOR_TYPE is the one label present on every Fission-managed
// pod regardless of executor type or specialization state
// (getEnvironmentPoolLabels / labelsForFunction in poolmgr, and the
// newdeploy/container pod templates). A bare Exists match would also admit
// any future non-Fission workload that happens to carry the same label key
// with an unrecognized value; selection.In against the closed set of known
// ExecutorType values ({poolmgr, newdeploy, container}) is least-privilege —
// it scopes the informer to Fission's own pods and nothing else.
//
// Namespace scoping mirrors routerCacheOptions: cluster tenancy watches
// cluster-wide (functions, and hence their pods/Services, can live in any
// namespace); other modes scope to poolWatchNamespaces().
func poolCacheOptions(base crcache.Options) (crcache.Options, error) {
	podReq, err := labels.NewRequirement(fv1.EXECUTOR_TYPE, selection.In, []string{
		string(fv1.ExecutorTypePoolmgr),
		string(fv1.ExecutorTypeNewdeploy),
		string(fv1.ExecutorTypeContainer),
	})
	if err != nil {
		return base, fmt.Errorf("building pod executor-type selector: %w", err)
	}
	sliceByObject := crcache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE}),
	}
	podByObject := crcache.ByObject{
		Label: labels.NewSelector().Add(*podReq),
		// Strip each cached Pod to only what PodSummary (pool.go) reads: its
		// identity + labels + a slice of Status. Dropping Spec (container env
		// values included) both bounds the cache footprint and stops the
		// agentruntime from retaining pod env — which can carry secrets — in
		// memory. Identity fields (Name/Namespace/UID/ResourceVersion) are kept
		// so the informer's own indexing and delta detection stay correct; the
		// transform is idempotent and passes through anything that is not a
		// *corev1.Pod (e.g. a cache.DeletedFinalStateUnknown tombstone).
		Transform: func(obj any) (any, error) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return obj, nil
			}
			return &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pod.Name,
					Namespace:       pod.Namespace,
					UID:             pod.UID,
					ResourceVersion: pod.ResourceVersion,
					Labels:          pod.Labels,
				},
				Status: corev1.PodStatus{
					PodIP:      pod.Status.PodIP,
					Phase:      pod.Status.Phase,
					Conditions: pod.Status.Conditions,
				},
			}, nil
		},
	}
	if !utils.ClusterTenancyEnabled() {
		nsConfig := map[string]crcache.Config{}
		for _, ns := range poolWatchNamespaces() {
			nsConfig[ns] = crcache.Config{}
		}
		sliceByObject.Namespaces = nsConfig
		podByObject.Namespaces = nsConfig
	}
	base.ByObject = map[client.Object]crcache.ByObject{
		&discoveryv1.EndpointSlice{}: sliceByObject,
		&corev1.Pod{}:                podByObject,
	}
	return base, nil
}

// poolSARRetryDelay is the delay between checkPoolRBAC's SAR retry attempts,
// mirroring router.go's sarRetryDelay: a boot-time apiserver flake must not
// permanently degrade pool introspection for the replica's whole lifetime.
const poolSARRetryDelay = 2 * time.Second

// poolRBACCheck names one (group, resource) pair checkPoolRBAC verifies
// list+watch for.
type poolRBACCheck struct{ group, resource string }

// checkPoolRBAC verifies — with an actionable error — that the agent runtime
// can list and watch EndpointSlices and Pods in every namespace the pool
// introspection cache would watch. Mirrors checkSliceWatchRBAC (router.go:
// 130-145): without this preflight a missing Role leaves the manager cache
// retrying a forbidden LIST forever, wedging the whole replica not-ready
// (dispatch included, since it shares one Manager) rather than degrading
// just the pool panel. Callers degrade GET /registry/pool to a 503 on error
// rather than exiting: pool introspection is a read-only UI feature with no
// effect on turn dispatch, and crash-looping the whole subsystem over a
// missing optional grant would turn an optional feature into an outage.
func checkPoolRBAC(ctx context.Context, kubeClient kubernetes.Interface) error {
	namespaces := poolWatchNamespaces()
	if utils.ClusterTenancyEnabled() {
		namespaces = []string{""}
	}
	checks := []poolRBACCheck{
		{group: "discovery.k8s.io", resource: "endpointslices"},
		{group: "", resource: "pods"},
	}
	for _, ns := range namespaces {
		for _, chk := range checks {
			for _, verb := range []string{"list", "watch"} {
				res, err := poolWatchSAR(ctx, kubeClient, ns, chk.group, chk.resource, verb)
				if err != nil {
					return fmt.Errorf("error checking %s RBAC in namespace %q: %w", chk.resource, ns, err)
				}
				if !res.Status.Allowed {
					return fmt.Errorf("agentruntime is not allowed to %s %s in namespace %q (reason: %s); "+
						"grant get/list/watch on endpointslices (discovery.k8s.io) and pods to the agentruntime "+
						"ServiceAccount to enable pool introspection", verb, chk.resource, ns, res.Status.Reason)
				}
			}
		}
	}
	return nil
}

// poolWatchSAR issues one SelfSubjectAccessReview for verb+resource in ns,
// retrying transient apiserver errors up to 3 times total (mirrors
// sliceWatchSAR, router.go:213-238).
func poolWatchSAR(ctx context.Context, kubeClient kubernetes.Interface, ns, group, resource, verb string) (*authorizationv1.SelfSubjectAccessReview, error) {
	sar := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: ns,
				Verb:      verb,
				Group:     group,
				Resource:  resource,
			},
		},
	}
	var res *authorizationv1.SelfSubjectAccessReview
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(poolSARRetryDelay):
			}
		}
		res, err = kubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
		if err == nil {
			break
		}
	}
	return res, err
}

const (
	// envAllowInsecure, when "true", permits the agent runtime to start
	// without a signing key (every caller gets a wildcard namespace scope).
	// Without it, an empty JWT_SIGNING_KEY is a hard startup error so the
	// endpoint fails closed rather than silently dispatching every caller's
	// turns unauthenticated.
	envAllowInsecure = "AGENT_ALLOW_INSECURE"

	// envSweepInterval / envArchiveRetention configure the Sweeper. Both are
	// read once here (never in a library constructor) per the deterministic-
	// constructor convention.
	envSweepInterval    = "AGENT_SWEEP_INTERVAL"
	envArchiveRetention = "AGENT_ARCHIVE_RETENTION"

	// envMaxContinuations configures the Dispatcher's yield=continue
	// self-chaining cap. 0 means unlimited (see Dispatcher.maxContinuations).
	envMaxContinuations = "AGENT_MAX_CONTINUATIONS"

	// envMaxSpawnDepth configures the Dispatcher's spawn-chain depth cap
	// (see Dispatcher.maxSpawnDepth / resolveParentage / ErrSpawnDepthExceeded).
	// Unset uses defaultMaxSpawnDepth (3). This is a DELIBERATE divergence
	// from envMaxContinuations: there, 0 means unlimited; here, an explicit
	// 0 means "no spawning" — every parented create is rejected. The cap
	// guards spawn-tree resource exhaustion, so "unlimited" is never the
	// right unset-equivalent default for this particular knob. What makes
	// "unset -> 3" and "explicit 0 -> 0 (none)" distinguishable at all is
	// envInt64's own contract: it substitutes the default only when the env
	// var is EMPTY, and parses an explicit "0" as the int64 0, not as a
	// signal to fall back.
	envMaxSpawnDepth = "AGENT_MAX_SPAWN_DEPTH"

	// envMaxSessionsDefault is the platform-wide live-session ceiling applied
	// to an agent whose spec sets no MaxSessions of its own. 0 means no default
	// (unbounded). Read once here, like envMaxContinuations.
	envMaxSessionsDefault = "AGENT_MAX_SESSIONS_DEFAULT"

	// envRegistryPoll configures the SSE registry feed's Poller cadence
	// (see events.go). Read once here, like envSweepInterval.
	envRegistryPoll = "AGENT_REGISTRY_POLL"

	// envStoragesvcURL configures the session workspace routes' (workspace.go)
	// upstream storagesvc base URL. Confirmed ABSENT from the agentruntime
	// deployment as of this slice (chart wiring is a later slice) — empty
	// means the workspace routes are DISABLED (503 "workspace disabled"),
	// deliberately never guessed from a convention like
	// "http://storagesvc.<namespace>": a wrong guess would silently point
	// artifact traffic at the wrong service rather than failing loudly.
	envStoragesvcURL = "STORAGESVC_URL"

	// envMaxArtifactBytes / envMaxSessionArtifactBytes / envMaxSessionArtifacts
	// configure WorkspaceHandler's per-artifact byte cap, per-session byte
	// budget, and per-session artifact-COUNT budget (workspace.go's handlePut
	// doc — the count budget is what closes the zero-byte-artifact bypass a
	// bytes-only budget leaves open, and what structurally bounds a session's
	// LIST response size). Read once here, like the other agent runtime knobs.
	envMaxArtifactBytes        = "AGENT_MAX_ARTIFACT_BYTES"
	envMaxSessionArtifactBytes = "AGENT_MAX_SESSION_ARTIFACT_BYTES"
	envMaxSessionArtifacts     = "AGENT_MAX_SESSION_ARTIFACTS"

	// defaultSweepInterval / defaultArchiveRetention are applied when the
	// corresponding env var is unset.
	defaultSweepInterval    = 30 * time.Second
	defaultArchiveRetention = 168 * time.Hour // 7 days

	// defaultMaxArtifactBytes / defaultMaxSessionArtifactBytes /
	// defaultMaxSessionArtifacts are applied when the corresponding env var
	// is unset. 0 for either session budget means unlimited (see
	// WorkspaceHandler.maxSessionArtifactBytes/maxSessionArtifacts); the
	// per-artifact cap has no such "0 means unlimited" escape hatch — it
	// always bounds a single PUT.
	defaultMaxArtifactBytes        int64 = 32 << 20
	defaultMaxSessionArtifactBytes int64 = 256 << 20
	defaultMaxSessionArtifacts     int64 = 1000

	// defaultRegistryPollInterval is applied when envRegistryPoll is unset.
	defaultRegistryPollInterval = 2 * time.Second

	// defaultMaxContinuations is applied when envMaxContinuations is unset.
	defaultMaxContinuations = 1000

	// defaultMaxSpawnDepth is applied when envMaxSpawnDepth is unset. See
	// envMaxSpawnDepth's doc comment for why an explicit 0 is NOT the same
	// as unset.
	defaultMaxSpawnDepth = 3

	// defaultMaxSessionsDefault is applied when envMaxSessionsDefault is unset:
	// a conservative platform ceiling that bounds unqualified session creation
	// without an operator having to set a per-agent MaxSessions.
	defaultMaxSessionsDefault = 1000

	// dispatcherMaxIdleConnsPerHost bounds the dispatcher's outbound
	// connection pool to the single router-internal host (see
	// httpx.PooledTransport) — sized like the workflow invoker's worker pool,
	// since both are internal clients driving one hot upstream.
	dispatcherMaxIdleConnsPerHost = 64
)

// Options configures Start. The listener is either pre-bound by the caller
// (Listener — e.g. a test harness binding 127.0.0.1:0) or bound here from
// Port.
type Options struct {
	// Port is the agent runtime's port. Ignored when Listener is set.
	Port int
	// Listener optionally pre-binds the listener.
	Listener net.Listener
	// RouterInternalURL is the resolved ROUTER_INTERNAL_URL passed down from
	// fission-bundle (the same value kubewatcher/timer/mqt/mcp receive), so
	// library constructors stay deterministic for unit tests.
	RouterInternalURL string
}

// Start runs the agent runtime subsystem. It builds a non-leader-elected,
// namespace-scoped cache manager over Functions (every replica serves its own
// AgentView, so reconcile must run on each), registers the agent reconciler,
// and serves the dispatch endpoint on Port until ctx is cancelled.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger,
	mgr *errgroup.Group, opts Options) error {
	logger = logger.WithName("agentruntime")

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kube client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}
	if err := crd.WaitForFunctionCRDs(ctx, logger, fissionClient); err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	// Env reads live here, never in constructors (deterministic-constructor
	// convention): the statestore DSN, the sweep/retention policy, and the
	// internal-auth master secret.
	opened, err := statestore.Open(ctx, statestore.FromEnv())
	if err != nil {
		return fmt.Errorf("opening statestore: %w", err)
	}
	caps := statestore.NewScoped(opened, nil)
	// caps ownership is handed to the Serve goroutine below, which closes it
	// only AFTER httpserver.Serve has fully drained — so the mux never serves a
	// request against a closed statestore during the shutdown window. This
	// defer is the fallback for the early-error paths between here and that
	// hand-off (statestore capabilities, env parsing, manager construction),
	// where the Serve goroutine is never started; capsClosed suppresses it once
	// ownership has transferred.
	capsClosed := false
	defer func() {
		if !capsClosed {
			_ = caps.Close()
		}
	}()

	kv, err := caps.KV()
	if err != nil {
		return fmt.Errorf("statestore KV capability: %w", err)
	}
	queue, err := caps.Queue()
	if err != nil {
		return fmt.Errorf("statestore Queue capability: %w", err)
	}
	eventLog, err := caps.EventLog()
	if err != nil {
		return fmt.Errorf("statestore EventLog capability: %w", err)
	}

	sweepInterval, err := envDuration(envSweepInterval, defaultSweepInterval)
	if err != nil {
		return err
	}
	retention, err := envDuration(envArchiveRetention, defaultArchiveRetention)
	if err != nil {
		return err
	}
	maxContinuations, err := envInt64(envMaxContinuations, defaultMaxContinuations)
	if err != nil {
		return err
	}
	maxSpawnDepth, err := envInt64(envMaxSpawnDepth, defaultMaxSpawnDepth)
	if err != nil {
		return err
	}
	registryPollInterval, err := envDuration(envRegistryPoll, defaultRegistryPollInterval)
	if err != nil {
		return err
	}
	maxSessionsDefault, err := envInt64(envMaxSessionsDefault, defaultMaxSessionsDefault)
	if err != nil {
		return err
	}
	maxArtifactBytes, err := envInt64(envMaxArtifactBytes, defaultMaxArtifactBytes)
	if err != nil {
		return err
	}
	maxSessionArtifactBytes, err := envInt64(envMaxSessionArtifactBytes, defaultMaxSessionArtifactBytes)
	if err != nil {
		return err
	}
	maxSessionArtifacts, err := envInt64(envMaxSessionArtifacts, defaultMaxSessionArtifacts)
	if err != nil {
		return err
	}

	// Fail closed: refuse to serve unauthenticated unless explicitly opted
	// in. Pass-through grants every caller a wildcard namespace scope and can
	// dispatch a turn to any agent-enabled Function via the internal
	// listener, so it must be a deliberate choice, not the consequence of a
	// missing key.
	authz := NewAuthorizer([]byte(os.Getenv("JWT_SIGNING_KEY")))
	allowInsecure, _ := strconv.ParseBool(os.Getenv(envAllowInsecure))
	if !authz.Enabled() && !allowInsecure {
		return fmt.Errorf("refusing to start agent runtime without authentication: set JWT_SIGNING_KEY to scope agent access, or %s=true to explicitly run the endpoint unauthenticated (dev only)", envAllowInsecure)
	}

	// The G16 agent-identity bearer (IdentityOrJWT, identity.go) is the
	// dispatch route's second, exclusive auth path for a pod's own spawn/
	// dispatch calls. Read RAW here (never hmacauth.DecodeKeyFromEnv, which
	// is for already-derived keys) — see IdentityVerifier's doc comment.
	// identityMaster/identityMasterOld are only ever consulted when authz is
	// Enabled(): see identityVerifier below.
	identityMaster := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	identityMasterOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))

	// Invocations go to the router internal listener, signed exactly like the
	// other publishers (timer/mqtrigger/workflow/mcp).
	var rt http.RoundTripper = otelhttp.NewTransport(httpx.PooledTransport(dispatcherMaxIdleConnsPerHost))
	if master := storagesvcClient.HMACSecretFromEnv(); len(master) > 0 {
		rt = hmacauth.ServiceSigner(master, hmacauth.ServiceRouterInternal, rt, time.Now)
	}

	view := NewAgentView()
	store := NewSessionStore(kv, time.Now)
	// hist is constructed here (over the same direct statestore handles as
	// store/queue) and threaded into RegistryAPI below for the read-only
	// history/checkpoint HTTP surface; it has no append path, so it is not a
	// dispatcher dependency.
	hist := NewHistoryStore(eventLog, kv)
	dispatcher := NewDispatcher(logger.WithName("dispatcher"), view, store, &http.Client{Transport: rt}, opts.RouterInternalURL, retention, time.Now, maxContinuations, maxSpawnDepth)

	// Session workspace routes (workspace.go): a SMALL internal client to
	// storagesvc's /v1/workspace surface, built the same way the dispatcher's
	// own router-internal client is above (otelhttp transport, master-signed
	// hmacauth.ServiceSigner) rather than extending pkg/storagesvc/client
	// (whose ClientInterface targets the archive upload/download contract).
	// wsClient stays nil when STORAGESVC_URL is unset — see envStoragesvcURL's
	// doc comment — which is what makes every workspace route answer 503
	// "workspace disabled" instead of agentruntime guessing a URL. Constructed
	// BEFORE the sweeper below so the same client doubles as the sweeper's
	// archive-time workspace-purge dependency (sweeper.go's workspacePurger).
	var wsClient *workspaceClient
	if storagesvcURL := os.Getenv(envStoragesvcURL); storagesvcURL != "" {
		var wsrt http.RoundTripper = otelhttp.NewTransport(httpx.PooledTransport(workspaceIdleConnsPerHost))
		if master := storagesvcClient.HMACSecretFromEnv(); len(master) > 0 {
			wsrt = hmacauth.ServiceSigner(master, hmacauth.ServiceStoragesvc, wsrt, time.Now)
		}
		wsClient = newWorkspaceClient(storagesvcURL, wsrt)
	}
	wsHandler := NewWorkspaceHandler(logger.WithName("workspace"), view, store, wsClient, retention, maxArtifactBytes, maxSessionArtifactBytes, maxSessionArtifacts)

	// wsPurger is a workspacePurger interface value, NOT the *workspaceClient
	// pointer directly: passing a typed-nil *workspaceClient through the
	// interface parameter would make the sweeper's `s.wsPurger == nil` check
	// (sweeper.go) false even when wsClient is nil, since a non-nil interface
	// type wrapping a nil pointer is itself a non-nil interface value. This
	// explicit nil-to-nil-interface conversion is what actually disables the
	// sweeper's purge when STORAGESVC_URL is unset.
	var wsPurger workspacePurger
	if wsClient != nil {
		wsPurger = wsClient
	}
	sweeper := NewSweeper(logger.WithName("sweeper"), view, store, hist, wsPurger, sweepInterval, retention, time.Now)

	// SSE registry feed: a single background Poller diffs
	// SessionStore.List across view.List() agents and publishes changes to a
	// Broadcaster; EventsHandler serves GET /registry/events from it. See
	// events.go's package doc for why this is poll-diff rather than
	// push-on-write, and its own doc comment for why it is mounted bare on
	// the mux below instead of behind authz middleware.
	registryBus := NewBroadcaster()
	registryPoller := NewPoller(logger.WithName("registry_events"), view, store, registryBus, registryPollInterval)
	// ctx is the subsystem's lifetime: threaded into the events handler so an
	// open SSE stream ends promptly on shutdown instead of pinning the drain.
	eventsHandler := NewEventsHandler(ctx, registryPoller, registryBus, authz)

	// WakeService needs the Dispatcher (to call DispatchTurn), so it is built
	// after the Dispatcher and wired back in via SetWakeEnqueuer — see that
	// method's doc comment for the construction-cycle rationale. This MUST
	// happen before the mux starts serving turns.
	wake := NewWakeService(logger.WithName("wake"), queue, dispatcher, time.Now)
	wake.SetRand(rand.Float64)
	dispatcher.SetWakeEnqueuer(wake)

	// Pool introspection: a missing EndpointSlice/Pod RBAC grant must
	// degrade GET /registry/pool to a 503, never wedge the manager cache
	// sync (which would take turn dispatch down with it, since dispatch and
	// pool introspection share one Manager). Checked BEFORE manager
	// construction because the cache options below depend on the outcome —
	// mirrors router.go's ordering exactly (checkSliceWatchRBAC before
	// ctrl.NewManager).
	poolDegraded := false
	if rbacErr := checkPoolRBAC(ctx, kubeClient); rbacErr != nil {
		logger.Error(rbacErr, "disabling pool introspection (GET /registry/pool will report 503)")
		poolDegraded = true
	}
	agentruntimeCache := crmanager.FissionCacheOptions()
	if !poolDegraded {
		agentruntimeCache, err = poolCacheOptions(agentruntimeCache)
		if err != nil {
			return fmt.Errorf("building pool introspection cache options: %w", err)
		}
	}

	// No leader election: each replica maintains its own in-memory view and
	// serves dispatch from it, so each must run the reconciler. The Function
	// watch stays scoped to the Fission-watched namespaces (per-namespace
	// RBAC Roles forbid a cluster-wide watch), mirroring the mcp/router
	// managers; the Scheme is agentruntimeScheme (Ruling B) UNCONDITIONALLY,
	// not gated on poolDegraded — only the EndpointSlice/Pod ByObject cache
	// entries above are conditional.
	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 agentruntimeScheme,
		Cache:                  agentruntimeCache,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up agentruntime manager: %w", err)
	}

	r := NewAgentReconciler(logger.WithName("agent_reconciler"), crMgr.GetClient(), view, maxSessionsDefault)
	if err := controller.RegisterTenantScoped(crMgr, &fv1.Function{}, r, "agentruntime-function"); err != nil {
		return fmt.Errorf("error registering agentruntime function reconciler: %w", err)
	}

	// index feeds GET /registry/pool's endpoint breakdown and the
	// Dispatcher's best-effort CurrentPod prediction (endpointcache.
	// PredictSticky). Created unconditionally — an Index with no informer
	// feeding it is simply always-empty, which is exactly the degraded
	// behavior wanted when poolDegraded — and RegisterInformer is skipped
	// entirely in that case: registering it would call GetInformer for a type
	// the cache has no RBAC to LIST, wedging cache sync.
	index := endpointcache.NewIndex()
	var poolSynced cache.InformerSynced
	if !poolDegraded {
		poolSynced, err = endpointcache.RegisterInformer(ctx, crMgr, index, logger)
		if err != nil {
			return fmt.Errorf("error registering agentruntime endpointslice informer: %w", err)
		}
	}
	// SetEndpointIndex happens here (not next to SetWakeEnqueuer above)
	// because index only exists once the manager — and the RBAC preflight
	// deciding whether it is ever fed — has been set up; see that method's
	// doc comment for why this ordering is safe (plain field, pre-serve
	// write). nil would also be a legal (prediction-disabled) value; passing
	// the always-safe empty-when-degraded Index instead keeps one code path
	// for both outcomes.
	dispatcher.SetEndpointIndex(index)
	poolAPI := NewPoolAPI(logger.WithName("pool_api"), crMgr.GetClient(), index, view, authz, func() bool {
		return poolSynced != nil && poolSynced()
	}, func() bool { return poolDegraded })

	// The sweeper runs as a manager runnable so it starts after cache sync
	// (a replica's view is empty until then anyway) and stops with the
	// manager.
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		sweeper.Run(rctx)
		return nil
	})); err != nil {
		return fmt.Errorf("adding agentruntime sweeper: %w", err)
	}

	// The registry-events poller runs as a manager runnable for the same
	// reason the sweeper does (start after cache sync, stop with the
	// manager); it feeds GET /registry/events (see events.go).
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		registryPoller.Run(rctx)
		return nil
	})); err != nil {
		return fmt.Errorf("adding agentruntime registry poller: %w", err)
	}

	// The wake service runs as a manager runnable for the same reason the
	// sweeper does (start after cache sync, stop with the manager); it
	// delivers the yield=continue self-chain the dispatcher enqueues via
	// SetWakeEnqueuer above.
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		wake.Run(rctx)
		return nil
	})); err != nil {
		return fmt.Errorf("adding agentruntime wake service: %w", err)
	}

	// ready flips true once the Function cache has synced (the manager starts
	// added runnables only after cache sync), so a replica reports ready only
	// after its view is being populated. /readyz additionally requires the
	// statestore to answer at probe time — a replica that cannot reach it
	// must not be serviced.
	var ready atomic.Bool
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		ready.Store(true)
		logger.Info("agentruntime function cache synced; dispatching turns")
		<-rctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("adding agentruntime readiness runnable: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "caches not yet synced", http.StatusServiceUnavailable)
			return
		}
		if err := caps.Ping(r.Context()); err != nil {
			// Log the detail server-side (a rollout-timeout in CI needs the
			// underlying host/driver error to diagnose), but return a generic
			// body: /readyz is unauthenticated, so it must not leak the
			// statestore host:port or driver to an anonymous caller.
			logger.Error(err, "readyz: statestore ping failed")
			http.Error(w, "statestore not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// identityVerifier is constructed only when authz.Enabled(): a nil
	// IdentityVerifier is what makes IdentityOrJWT's allowInsecure row a
	// bytewise no-op passthrough (see IdentityOrJWT's doc comment) —
	// identity headers must never be consulted, let alone 401 a caller over
	// a missing internal secret, when the whole endpoint is running
	// unauthenticated.
	var identityVerifier *IdentityVerifier
	if authz.Enabled() {
		identityVerifier = NewIdentityVerifier(identityMaster, identityMasterOld, view)
	}

	// The pattern here matches Dispatcher.Handler()'s own registration
	// exactly: the outer mux performs the {namespace}/{name} match (and sets
	// the path values authz.Middleware / IdentityOrJWT's identity path read)
	// before calling into the auth-wrapped dispatcher, which then matches the
	// same pattern again. IdentityOrJWT is the dispatch route's mount; the
	// session workspace routes below are the identity bearer's ONLY other
	// mount (via IdentityOrJWTOwnWorkspace, a STRICTER own-workspace-only
	// closure — see identity.go's verifyOwnWorkspace) — registry/SSE routes
	// stay on authz.Middleware/HTTPMiddleware directly, so the identity
	// bearer never reaches them.
	mux.Handle("POST /agents/{namespace}/{name}", IdentityOrJWT(identityVerifier, authz.Middleware)(dispatcher.Handler()))

	// Session workspace routes (workspace.go): PUT/GET/DELETE artifacts plus
	// a list route, mounted inside the SAME otel wrap as everything else
	// (handler := otelUtils.GetHandlerWithOTEL(mux, ...) below wraps the
	// whole mux, so registering here rather than separately is what keeps
	// these routes spanned). wsHandler itself answers 503 on every route when
	// wsClient is nil (STORAGESVC_URL unset).
	mountWorkspaceRoutes(mux, wsHandler, IdentityOrJWTOwnWorkspace(identityVerifier, authz.Middleware))

	// Registry read API: GET /registry/agents carries no
	// {namespace} path value, so it is mounted behind authz.HTTPMiddleware
	// (bearer verification only) and filters namespaces manually — see
	// RegistryAPI.ListAgents. The two per-agent routes below carry
	// {namespace}, so they use authz.Middleware exactly like the dispatch
	// route above.
	registryAPI := NewRegistryAPI(logger.WithName("registry_api"), view, store, hist, authz)
	mux.Handle("GET /registry/agents", authz.HTTPMiddleware(http.HandlerFunc(registryAPI.ListAgents)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions", authz.Middleware(http.HandlerFunc(registryAPI.ListSessions)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions/{id}", authz.Middleware(http.HandlerFunc(registryAPI.GetSession)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions/{id}/history", authz.Middleware(http.HandlerFunc(registryAPI.GetHistory)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions/{id}/checkpoint", authz.Middleware(http.HandlerFunc(registryAPI.GetCheckpoint)))

	// Pool introspection: pod/endpoint topology is cluster-operator
	// data, not per-agent data, so like ListAgents it carries no {namespace}
	// path value and is mounted behind authz.HTTPMiddleware with the
	// namespace filter applied manually inside PoolAPI.ServePool.
	mux.Handle("GET /registry/pool", authz.HTTPMiddleware(http.HandlerFunc(poolAPI.ServePool)))

	// SSE registry feed: mounted BARE, deliberately NOT behind
	// authz.HTTPMiddleware/Middleware — see EventsHandler's doc comment
	// (events.go) for why a browser EventSource forces auth to happen inside
	// the handler instead.
	mux.Handle("GET /registry/events", eventsHandler)

	// Boardroom UI: mounted UNAUTHENTICATED — it is a static asset
	// (embedded HTML/CSS/JS with no server-side data of its own); every API
	// it calls from the browser (GET /registry/agents, /registry/pool,
	// /registry/events, and the per-agent sessions routes above) enforces its
	// own bearer-token auth exactly as it would for any other caller.
	uiHandler := ui.Handler()
	mux.Handle("GET /ui", uiHandler)
	mux.Handle("GET /ui/", uiHandler)

	// caps is closed at the bottom of this function, not via defer and not
	// inside the Serve goroutine, so it outlives BOTH drain paths that can
	// still touch the statestore during shutdown: httpserver.Serve's graceful
	// HTTP drain (mgr.Go below) and crMgr.Start's background runnables
	// (sweeper, registry poller, and — the one that actually reaches the
	// statestore after ctx is cancelled — the wake consumer, whose settle/
	// dispatch bookkeeping deliberately runs on context.WithoutCancel(ctx) so
	// it can finish during the shutdown window). These two drains are
	// independent: Serve's server.Shutdown can return near-instantly on a
	// quiet shutdown, well before crMgr.Start's runnables finish, so closing
	// caps as soon as Serve drains (as a prior version of this code did) can
	// close the statestore out from under the wake consumer's detached
	// bookkeeping. serveDone signals only that the Serve goroutine has
	// returned; caps.Close() runs exactly once, after WAITING FOR BOTH
	// crMgr.Start(ctx) to return AND serveDone to close. (See the capsClosed
	// defer above for the early-error fallback this replaces.)
	//
	// Serve is given its own cancellable serveCtx, not ctx directly: on a
	// normal shutdown both are keyed off the same signal so this changes
	// nothing, but crMgr.Start(ctx) can also return a non-nil error (a
	// runnable or cache-sync failure) WITHOUT ctx being cancelled —
	// controller-runtime never cancels the ctx it was handed. httpserver.Serve
	// only ever proceeds past its internal <-ctx.Done() wait on cancellation,
	// so without stopServe() below, <-serveDone would block forever on that
	// error path and wedge this whole function (and caps.Close with it) even
	// though the process is still live and should report the error.
	// Server spans on the whole mux, mirroring the router's public-listener
	// precedent (router.go's otelUtils.GetHandlerWithOTEL(publicMR, "fission-
	// router", ...) wrap): otel wraps OUTSIDE every per-route auth wrapper
	// (IdentityOrJWT above, authz.Middleware/HTTPMiddleware below) so an
	// unauthenticated 401/403 still gets a span — deliberately inverting the
	// router-INTERNAL listener's verifier-outside ordering, which exists
	// there to keep an unsigned request from reaching the proxy at all, not
	// to hide its span.
	//
	// Filtered: /registry/events (the SSE registry feed — a span living for
	// the lifetime of an open stream is not useful), /ui (the boardroom
	// UI, both the bare GET /ui route and everything under GET /ui/ — the
	// otelUtils.UrlsToIgnore filter is HasPrefix, so the single "/ui" entry
	// covers both routes registered above), and /healthz + /readyz (kubelet
	// liveness/readiness probes, registered above as the exact paths
	// "/healthz" / "/readyz" with no trailing-slash variant — a span on
	// every probe poll is pure noise, and the router's own public listener
	// filters its healthz endpoint the same way, otelUtils.UrlsToIgnore
	// ("/router-healthz"), router.go). otelhttp v0.70.0's middleware
	// short-circuits to next.ServeHTTP BEFORE span creation AND before
	// propagator extraction whenever a filter returns false (handler.go
	// :90-98) — a filtered request is not merely un-spanned, it never even
	// sees an extracted trace context, which is the behavior wanted here.
	handler := otelUtils.GetHandlerWithOTEL(mux, "fission-agentruntime", otelUtils.UrlsToIgnore("/registry/events", "/ui", "/healthz", "/readyz"))

	capsClosed = true
	serveCtx, stopServe := context.WithCancel(ctx)
	serveDone := make(chan struct{})
	mgr.Go(func() error {
		defer close(serveDone)
		httpserver.Serve(serveCtx, logger, mgr, httpserver.ServerOptions{
			Name: "agentruntime", Addr: strconv.Itoa(opts.Port), Listener: opts.Listener, Handler: handler,
		})
		return nil
	})

	if authz.Enabled() {
		logger.Info("starting agentruntime server", "port", opts.Port, "authEnabled", true)
	} else {
		// Pass-through mode grants every caller a wildcard namespace scope.
		// Explicitly opted in via AGENT_ALLOW_INSECURE; loud so it is never
		// mistaken for a scoped deployment.
		logger.Info("WARNING: starting agentruntime server with authentication DISABLED — every caller can dispatch turns to any agent-enabled function (set JWT_SIGNING_KEY to scope access)", "port", opts.Port)
	}
	mgrErr := crMgr.Start(ctx)
	// stopServe unblocks Serve's <-ctx.Done() wait even when ctx is still
	// live (see the comment above serveCtx). On a normal shutdown ctx is
	// already cancelled by the time crMgr.Start returns, so this is a no-op;
	// it must run before the receive below, not as a deferred call after it.
	stopServe()
	<-serveDone
	_ = caps.Close()
	return mgrErr
}

// envDuration reads key as a time.Duration, returning def when it is unset.
// A set-but-unparseable value is a startup error, not a silent fallback to
// def — a typo'd AGENT_SWEEP_INTERVAL should fail loudly, not run the
// sweeper on a default cadence nobody chose.
func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("parsing %s=%q: %w", key, v, err)
	}
	return d, nil
}

// envInt64 reads key as an int64, returning def when it is unset. A
// set-but-unparseable value is a startup error, not a silent fallback to def
// — mirrors envDuration's rationale.
func envInt64(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s=%q: %w", key, v, err)
	}
	return n, nil
}
