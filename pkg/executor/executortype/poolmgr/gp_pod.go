// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/maps"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

func (gp *GenericPool) getEnvironmentPoolLabels(env *fv1.Environment) map[string]string {
	envLabels := maps.CopyStringMap(env.Labels)
	envLabels[fv1.EXECUTOR_TYPE] = string(fv1.ExecutorTypePoolmgr)
	envLabels[fv1.ENVIRONMENT_NAME] = env.Name
	envLabels[fv1.ENVIRONMENT_NAMESPACE] = env.Namespace
	envLabels[fv1.ENVIRONMENT_UID] = string(env.UID)
	envLabels["managed"] = "true" // this allows us to easily find pods managed by the deployment
	if gp.ociImageHash != "" {
		// Routes this pool's warm pods to its own readyPodQueue and keeps
		// the plain pool's seed/selectors from picking them up.
		envLabels[fv1.POOL_OCI_IMAGE_HASH] = gp.ociImageHash
	}
	return envLabels
}

func (gp *GenericPool) getDeployAnnotations(env *fv1.Environment) map[string]string {
	deployAnnotations := maps.CopyStringMap(env.Annotations)
	deployAnnotations[fv1.EXECUTOR_INSTANCEID_LABEL] = gp.instanceID
	return deployAnnotations
}

// choosePod picks a ready pod from the pool and relabels it, waiting if necessary.
// returns the key and pod API object.
func (gp *GenericPool) choosePod(ctx context.Context, newLabels map[string]string) (string, *apiv1.Pod, error) {
	startTime := time.Now()
	podTimeout := startTime.Add(gp.podReadyTimeout)
	deadline, ok := ctx.Deadline()
	if ok {
		deadline = deadline.Add(-1 * time.Second)
		if deadline.Before(podTimeout) {
			podTimeout = deadline
		}
	}
	expoDelay := 100 * time.Millisecond
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger)
	// The executor Manager cache is synced before the API server serves, so no
	// per-pool cache-sync wait is needed here anymore.
	for {
		// Retries took too long, error out.
		if time.Now().After(podTimeout) {
			logger.Info("timed out waiting for pod", "labels", newLabels, "timeout", podTimeout.Sub(startTime))
			return "", nil, errors.New("timeout: waited too long to get a ready pod")
		}
		if ctx.Err() != nil {
			logger.Error(ctx.Err(), "context canceled while waiting for pod", "labels", newLabels, "timeout", podTimeout.Sub(startTime))
			return "", nil, fmt.Errorf("context canceled while waiting for pod: %w", ctx.Err())
		}

		var chosenPod *apiv1.Pod

		otelUtils.SpanTrackEvent(ctx, "waitForPod", otelUtils.MapToAttributes(newLabels)...)
		key, quit := gp.readyPodQueue.Get()
		if quit {
			logger.Error(nil, "readypod controller is not running")
			return "", nil, errors.New("readypod controller is not running")
		}
		logger.V(1).Info("got key from the queue", "key", key)
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			logger.Error(err, "error splitting key", "key", key)
			gp.readyPodQueue.Done(key)
			return "", nil, err
		}
		pod := &apiv1.Pod{}
		if err := gp.crClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pod); err != nil {
			// Not in the cache (e.g. already specialized and relabelled out of the
			// poolmgr-managed set, or deleted): skip this stale key.
			logger.V(1).Info("ready pod not found in cache; skipping", "key", key, "err", err)
			gp.readyPodQueue.Done(key)
			continue
		}
		// The Manager cache holds both warm (managed=true) and specialized
		// (managed=false) poolmgr pods; the old per-pool lister only held warm ones.
		// Skip any pod that has already been specialized so we never re-specialize
		// another function's pod.
		if pod.Labels["managed"] != "true" {
			logger.V(1).Info("pod already specialized; skipping", "key", key)
			gp.readyPodQueue.Done(key)
			continue
		}
		if utils.IsPodTerminated(pod) {
			logger.Error(nil, "pod is terminated", "key", key)
			gp.readyPodQueue.Done(key)
			continue
		}
		if !utils.IsReadyPod(pod) {
			// Normal transient: the generic pod is still warming up. It is
			// requeued with exponential backoff and re-checked, so this is an
			// expected retry, not an error (it was logged at Error with a nil
			// error, which flooded the executor log during pool warm-up).
			//
			// Distinguish "no container statuses yet" (the race that causes
			// fetcher i/o timeouts — kubelet has not reported container status
			// so the fetcher HTTP server may not be listening) from "container
			// reported but not Ready" (normal warmup) so the log line is
			// actionable when debugging. See ci-29472717703 v1.32.11 analysis.
			if len(pod.Status.ContainerStatuses) == 0 {
				logger.Info("pod not ready: containerStatuses empty (kubelet has not reported yet)",
					"key", key, "pod", pod.Name, "podIP", pod.Status.PodIP,
					"phase", pod.Status.Phase, "delay", expoDelay)
			} else {
				ready, total := utils.PodContainerReadyStatus(pod)
				logger.V(1).Info("pod not ready, pod will be checked again",
					"key", key, "delay", expoDelay,
					"readyContainers", ready, "totalContainers", total)
			}
			gp.readyPodQueue.Done(key)
			gp.readyPodQueue.AddAfter(key, expoDelay)
			expoDelay *= 2
			continue
		}
		chosenPod = pod.DeepCopy()
		otelUtils.SpanTrackEvent(ctx, "foundPod", otelUtils.GetAttributesForPod(chosenPod)...)

		if gp.env.Spec.AllowedFunctionsPerContainer != fv1.AllowedFunctionsPerContainerInfinite {
			// Relabel.  If the pod already got picked and
			// modified, this should fail; in that case just
			// retry.
			// Append executor instance id to pod annotations to
			// indicate this pod is managed by this executor.
			annotations := gp.getDeployAnnotations(gp.env)
			patch := map[string]any{
				"metadata": map[string]any{
					"annotations": annotations,
					"labels":      newLabels,
				},
			}
			patchBytes, _ := json.Marshal(patch)
			logger.Info("relabel pod", "pod", string((patchBytes)))
			newPod, err := gp.kubernetesClient.CoreV1().Pods(chosenPod.Namespace).Patch(ctx, chosenPod.Name, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
			if err != nil && errors.Is(err, context.Canceled) {
				// ending retry loop when the request canceled
				gp.readyPodQueue.Done(key)
				gp.readyPodQueue.AddAfter(key, expoDelay)
				return "", nil, fmt.Errorf("failed to relabel pod: %s", err)
			} else if err != nil {
				logger.Error(err, "failed to relabel pod", "pod", chosenPod.Name, "delay", expoDelay)
				gp.readyPodQueue.Done(key)
				gp.readyPodQueue.AddAfter(key, expoDelay)
				expoDelay *= 2
				continue
			}
			otelUtils.SpanTrackEvent(ctx, "podRelabel", otelUtils.GetAttributesForPod(chosenPod)...)

			// With StrategicMergePatchType, the client-go sometimes return
			// nil error and the labels & annotations remain the same.
			// So we have to check both of them to ensure the patch success.
			for k, v := range newLabels {
				if newPod.Labels[k] != v {
					return "", nil, fmt.Errorf("value of necessary labels '%s' mismatch: want '%s', get '%v'",
						k, v, newPod.Labels[k])
				}
			}
			for k, v := range annotations {
				if newPod.Annotations[k] != v {
					return "", nil, fmt.Errorf("value of necessary annotations '%s' mismatch: want '%s', get '%v'",
						k, v, newPod.Annotations[k])
				}
			}
		}

		// Re-touch the pool's activity clock at claim time: the wait above
		// can consume most of the pod-ready window, and the idle reaper
		// must never see a pool that just claimed a pod as idle.
		gp.lastActive.Store(time.Now().UnixNano())

		logger.Info("chose pod", "labels", newLabels,
			"pod", chosenPod.Name, "elapsed_time", time.Since(startTime))

		return key, chosenPod, nil
	}
}

func (gp *GenericPool) labelsForFunction(metadata *metav1.ObjectMeta) map[string]string {
	label := gp.getEnvironmentPoolLabels(gp.env)
	label[fv1.FUNCTION_NAME] = metadata.Name
	label[fv1.FUNCTION_UID] = string(metadata.UID)
	label[fv1.FUNCTION_NAMESPACE] = metadata.Namespace // function CRD must stay within same namespace of environment CRD
	label["managed"] = "false"                         // this allows us to easily find pods not managed by the deployment
	return label
}

// specializedPodLabels is labelsForFunction plus the function-generation label
// (RFC-0002). Used for the choosePod relabel only — listing/selection paths
// (RefreshFuncPods, the legacy useSvc/istio selectors) keep labelsForFunction so
// they still match pods of every generation.
func (gp *GenericPool) specializedPodLabels(metadata *metav1.ObjectMeta) map[string]string {
	label := gp.labelsForFunction(metadata)
	label[fv1.FUNCTION_GENERATION] = strconv.FormatInt(metadata.Generation, 10)
	return label
}

func (gp *GenericPool) scheduleDeletePod(ctx context.Context, name string) {
	// The sleep allows debugging or collecting logs from the pod before it's
	// cleaned up.  (We need a better solutions for both those things; log
	// aggregation and storage will help.)
	gp.logger.Info("error in pod - scheduling cleanup", "pod", name)
	err := gp.kubernetesClient.CoreV1().Pods(gp.fnNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		gp.logger.Error(err,
			"error deleting pod", "name", name,
			"namespace", gp.fnNamespace,
		)
	}
}
