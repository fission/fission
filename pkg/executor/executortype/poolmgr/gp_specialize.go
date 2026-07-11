// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// IsIPv6 validates if the podIP follows to IPv6 protocol
func IsIPv6(podIP string) bool {
	ip := net.ParseIP(podIP)
	return ip != nil && strings.Contains(podIP, ":")
}

func (gp *GenericPool) getFetcherURL(podIP string) string {
	testURL := os.Getenv("TEST_FETCHER_URL")
	if len(testURL) != 0 {
		// it takes a second or so for the test service to
		// become routable once a pod is relabeled. This is
		// super hacky, but only runs in unit tests.
		time.Sleep(5 * time.Second)
		return testURL
	}

	isv6 := IsIPv6(podIP)
	var baseURL string

	if isv6 { // We use bracket if the IP is in IPv6.
		baseURL = fmt.Sprintf("http://[%s]:%d/", podIP, svcinfo.PortFetcher)
	} else {
		baseURL = fmt.Sprintf("http://%s:%d/", podIP, svcinfo.PortFetcher)
	}
	return baseURL
}

// shouldStampNamespaceKeyScheme reports whether a pool pod created in fnNamespace
// should carry the namespace key-scheme annotation. Only when per-namespace keys
// are in use (dynamic or cluster mode) and the namespace is a live tenant: the
// tenant controller provisions a namespace's derived-key Secret before admitting
// it to the live set, so a live tenant is guaranteed to have one — the pod will
// mount its per-namespace key and the executor (fetcherSigningNamespace) will
// ns-sign it. Stamping a namespace without that Secret would promise a key the pod
// never receives and 401 every specialization, so the IsTenant gate is
// load-bearing, not cosmetic.
func shouldStampNamespaceKeyScheme(fnNamespace string, resolver *utils.NamespaceResolver) bool {
	return utils.PerNamespaceKeysEnabled() && resolver != nil && resolver.IsTenant(fnNamespace)
}

// fetcherSigningNamespace decides how the executor signs the /specialize call to
// a pod's fetcher. A pod the executor stamped with the namespace key-scheme
// annotation (genDeploymentSpec, only while per-namespace keys are in use for the
// pod's namespace) holds only its per-namespace derived key and verifies with it,
// so we sign with ServiceSignerNS for the pod's namespace and nsScoped is true.
// Every other pod — pre-upgrade pods carrying no annotation across a rolling
// upgrade, or any pod under static tenancy — verifies with the master-derived
// key, so nsScoped is false and the caller master-signs. The per-namespace-keys
// gate makes a stale annotation harmless if the feature is turned back off.
func fetcherSigningNamespace(pod *apiv1.Pod) (namespace string, nsScoped bool) {
	if utils.PerNamespaceKeysEnabled() && fv1.HasNamespaceKeyScheme(pod.Annotations) {
		return pod.Namespace, true
	}
	return "", false
}

// specializePod runs the specialization inside a cold-start child span
// (RFC-0015): on failure the span is marked errored with the failure reason, so
// a trace shows specialization as the failing phase (the error-biased sampler
// guarantees such a trace is recorded even when head sampling would drop it).
func (gp *GenericPool) specializePod(ctx context.Context, pod *apiv1.Pod, fn *fv1.Function) error {
	ctx, span := otel.Tracer("fission-executor").Start(ctx, "coldstart/specialize")
	span.SetAttributes(otelUtils.GetAttributesForFunction(fn)...)
	defer span.End()

	err := gp.doSpecializePod(ctx, pod, fn)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, ferror.ReasonSpecializationFailed)
		span.SetAttributes(attribute.String("coldstart.failure_reason", ferror.ReasonSpecializationFailed))
	}
	return err
}

// existsInFnNamespace confirms a function-referenced Secret/ConfigMap is present
// in the function namespace. It reads through the executor Manager's cache
// (crClient) to keep this pre-flight check off the API server on the cold path,
// and falls back to a direct API read on any cache miss or error — so a
// just-created object the informer cache hasn't observed yet (or a namespace
// outside the cache's scope) is confirmed authoritatively rather than spuriously
// reported missing. cached must be an empty object of the type being checked.
func (gp *GenericPool) existsInFnNamespace(ctx context.Context, cached client.Object, name string, directGet func(context.Context) error) (bool, error) {
	if err := gp.crClient.Get(ctx, client.ObjectKey{Namespace: gp.fnNamespace, Name: name}, cached); err == nil {
		return true, nil
	}
	// Cache miss / unwatched namespace / unsynced cache: confirm against the API
	// server before concluding the object is missing.
	err := directGet(ctx)
	if err == nil {
		return true, nil
	}
	if k8s_err.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// doSpecializePod chooses a pod, copies the required user-defined function to that pod
// (via fetcher), and calls the function-run container to load it, resulting in a
// specialized pod.
func (gp *GenericPool) doSpecializePod(ctx context.Context, pod *apiv1.Pod, fn *fv1.Function) error {
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger)

	// for fetcher we don't need to create a service, just talk to the pod directly
	podIP := pod.Status.PodIP
	if len(podIP) == 0 {
		return fmt.Errorf("pod %s in namespace %s has no IP", pod.Name, pod.Namespace)
	}

	// Fetcherless Path B pods (B-direct) have no fetcher to relay through:
	// the code is already mounted, so go straight to the env's load
	// endpoint. B-fetcher pods (RFC-0012) fall through to the normal
	// fetcher path below — the fetcher's exists-early-exit makes the fetch
	// a no-op against the image mount, and it still materializes
	// Secrets/ConfigMaps and drives the load.
	if gp.oci != nil && !gp.ociFetcherVariant {
		return gp.loadOnlySpecialize(ctx, podIP, fn)
	}
	for _, cm := range fn.Spec.ConfigMaps {
		exists, err := gp.existsInFnNamespace(ctx, &apiv1.ConfigMap{}, cm.Name, func(ctx context.Context) error {
			_, e := gp.kubernetesClient.CoreV1().ConfigMaps(gp.fnNamespace).Get(ctx, cm.Name, metav1.GetOptions{})
			return e
		})
		if err != nil {
			return err
		}
		if !exists {
			logger.Error(nil, "configmap namespace mismatch", "error", "configmap must be in same namespace as function namespace",
				"configmap_name", cm.Name,
				"configmap_namespace", cm.Namespace,
				"function_name", fn.Name,
				"function_namespace", gp.fnNamespace)
			return fmt.Errorf("configmap %s must be in same namespace as function namespace", cm.Name)
		}
	}
	for _, sec := range fn.Spec.Secrets {
		exists, err := gp.existsInFnNamespace(ctx, &apiv1.Secret{}, sec.Name, func(ctx context.Context) error {
			_, e := gp.kubernetesClient.CoreV1().Secrets(gp.fnNamespace).Get(ctx, sec.Name, metav1.GetOptions{})
			return e
		})
		if err != nil {
			return err
		}
		if !exists {
			logger.Error(nil, "secret namespace mismatch", "error", "secret must be in same namespace as function namespace",
				"secret_name", sec.Name,
				"secret_namespace", sec.Namespace,
				"function_name", fn.Name,
				"function_namespace", gp.fnNamespace)
			return fmt.Errorf("secret %s must be in same namespace as function namespace", sec.Name)
		}
	}
	// specialize pod with service
	if gp.useIstio {
		svc := utils.GetFunctionIstioServiceName(fn.Name, fn.Namespace)
		podIP = fmt.Sprintf("%s.%s", svc, gp.fnNamespace)
	}

	// tell fetcher to get the function.
	fetcherURL := gp.getFetcherURL(podIP)
	logger.Info("calling fetcher to copy function", "function", fn.Name, "url", fetcherURL)

	specializeReq := gp.fetcherConfig.NewSpecializeRequest(fn, gp.env)

	logger.Info("specializing pod", "function", fn.Name)

	// Fetcher will download user function to share volume of pod, and
	// invoke environment specialize api for pod specialization. The
	// HMAC master secret (when internalAuth is enabled) is read from
	// the executor pod's env so each /specialize call to the in-pod
	// fetcher carries the X-Fission-Auth-* headers required by the
	// fetcher's verifier; an empty secret is the explicit pass-through
	// for first-deploy installs. Version-aware: a pod created under
	// dynamic tenancy verifies with its per-namespace key, so sign with
	// that key; every other pod stays master-signed (see
	// fetcherSigningNamespace).
	master := storagesvcClient.HMACSecretFromEnv()
	var client fetcherClient.ClientInterface
	signNS, nsScoped := fetcherSigningNamespace(pod)
	if nsScoped {
		client = fetcherClient.MakeClientNS(gp.logger, fetcherURL, master, signNS)
	} else {
		client = fetcherClient.MakeClient(gp.logger, fetcherURL, master)
	}
	if err := client.Specialize(ctx, &specializeReq); err != nil {
		// Name the chosen signing scheme: a 401 here usually means the executor's
		// pick disagrees with the pod's mounted key (e.g. ns-signed but
		// FISSION_FETCHER_KEY not mounted), and the two halves live in different
		// processes — surfacing the scheme turns a log-correlation hunt into a read.
		if nsScoped {
			return fmt.Errorf("specialize signed namespace-scoped for %q (verify FISSION_FETCHER_KEY is mounted on the pod): %w", signNS, err)
		}
		return fmt.Errorf("specialize signed master-scoped: %w", err)
	}
	otelUtils.SpanTrackEvent(ctx, "specializedPod", otelUtils.GetAttributesForPod(pod)...)
	return nil
}
