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

	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
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
		baseURL = fmt.Sprintf("http://[%s]:8000/", podIP)
	} else {
		baseURL = fmt.Sprintf("http://%s:8000/", podIP)
	}
	return baseURL
}

// specializePod chooses a pod, copies the required user-defined function to that pod
// (via fetcher), and calls the function-run container to load it, resulting in a
// specialized pod.
func (gp *GenericPool) specializePod(ctx context.Context, pod *apiv1.Pod, fn *fv1.Function) error {
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger)

	// for fetcher we don't need to create a service, just talk to the pod directly
	podIP := pod.Status.PodIP
	if len(podIP) == 0 {
		return fmt.Errorf("pod %s in namespace %s has no IP", pod.Name, pod.Namespace)
	}

	// Path B (image-volume) pods have no fetcher to relay through: the code
	// is already mounted, so go straight to the env's load endpoint. The
	// eligibility check guarantees such functions carry no Secrets or
	// ConfigMaps and run v2+ environments.
	if gp.oci != nil {
		return gp.loadOnlySpecialize(ctx, podIP, fn)
	}
	for _, cm := range fn.Spec.ConfigMaps {
		_, err := gp.kubernetesClient.CoreV1().ConfigMaps(gp.fnNamespace).Get(ctx, cm.Name, metav1.GetOptions{})
		if err != nil {
			if k8s_err.IsNotFound(err) {
				logger.Error(nil, "configmap namespace mismatch", "error", "configmap must be in same namespace as function namespace",
					"configmap_name", cm.Name,
					"configmap_namespace", cm.Namespace,
					"function_name", fn.Name,
					"function_namespace", gp.fnNamespace)
				return fmt.Errorf("configmap %s must be in same namespace as function namespace", cm.Name)
			} else {
				return err
			}
		}
	}
	for _, sec := range fn.Spec.Secrets {
		_, err := gp.kubernetesClient.CoreV1().Secrets(gp.fnNamespace).Get(ctx, sec.Name, metav1.GetOptions{})
		if err != nil {
			if k8s_err.IsNotFound(err) {
				logger.Error(nil, "secret namespace mismatch", "error", "secret must be in same namespace as function namespace",
					"secret_name", sec.Name,
					"secret_namespace", sec.Namespace,
					"function_name", fn.Name,
					"function_namespace", gp.fnNamespace)
				return fmt.Errorf("secret %s must be in same namespace as function namespace", sec.Name)
			} else {
				return err
			}

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
	// for first-deploy installs.
	err := fetcherClient.MakeClient(gp.logger, fetcherURL, storagesvcClient.HMACSecretFromEnv()).Specialize(ctx, &specializeReq)
	if err != nil {
		return err
	}
	otelUtils.SpanTrackEvent(ctx, "specializedPod", otelUtils.GetAttributesForPod(pod)...)
	return nil
}
