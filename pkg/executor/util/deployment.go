// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// ScaleDeployment scales the named deployment to replicas via the scale
// subresource. Shared by the newdeploy and container managers, which both
// scale a per-function deployment up to MinScale on (re)specialization.
func ScaleDeployment(ctx context.Context, kubeClient kubernetes.Interface, logger logr.Logger, ns, name string, replicas int32) error {
	otelUtils.SpanTrackEvent(ctx, "scaleDeployment", otelUtils.MapToAttributes(map[string]string{
		"deployment-name":      name,
		"deployment-namespace": ns,
		"replicas":             fmt.Sprintf("%d", replicas),
	})...)
	logger = otelUtils.LoggerWithTraceID(ctx, logger)
	logger.Info("scaling deployment",
		"deployment", name,
		"namespace", ns,
		"replicas", replicas)
	_, err := kubeClient.AppsV1().Deployments(ns).UpdateScale(ctx, name, &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: autoscalingv1.ScaleSpec{
			Replicas: replicas,
		},
	}, metav1.UpdateOptions{})
	return err
}

// WaitForDeployment polls the deployment until it has at least replicas
// AvailableReplicas, or until specializationTimeout seconds elapse. Shared by
// the newdeploy and container managers. specializationTimeout is floored at
// fv1.DefaultSpecializationTimeOut. AvailableReplicas is used in preference to
// ReadyReplicas since the pods may not be able to serve network traffic yet.
func WaitForDeployment(ctx context.Context, kubeClient kubernetes.Interface, logger logr.Logger, depl *appsv1.Deployment, replicas int32, specializationTimeout int) (latestDepl *appsv1.Deployment, err error) {
	oldStatus := depl.Status
	otelUtils.SpanTrackEvent(ctx, "waitForDeployment", otelUtils.GetAttributesForDeployment(depl)...)
	// if no specializationTimeout is set, use default value
	if specializationTimeout < fv1.DefaultSpecializationTimeOut {
		specializationTimeout = fv1.DefaultSpecializationTimeOut
	}

	logger = otelUtils.LoggerWithTraceID(ctx, logger)

	for i := 0; i < specializationTimeout; i++ {
		latestDepl, err = kubeClient.AppsV1().Deployments(depl.ObjectMeta.Namespace).Get(ctx, depl.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		// TODO check for imagePullerror
		if latestDepl.Status.AvailableReplicas >= replicas {
			otelUtils.SpanTrackEvent(ctx, "deploymentAvailable", otelUtils.GetAttributesForDeployment(latestDepl)...)
			return latestDepl, err
		}
		// Sleep between polls, but stay responsive to cancellation (executor
		// shutdown / loss of leadership) instead of blocking for a full second.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}

	logger.Error(nil, "Deployment provision failed within timeout window", "name", latestDepl.Name, "old_status", oldStatus,
		"current_status", latestDepl.Status, "timeout", specializationTimeout)

	// this error appears in the executor pod logs
	timeoutError := fmt.Errorf("failed to create deployment within the timeout window of %d seconds", specializationTimeout)
	return nil, timeoutError
}

// ReferencedResourcesRVSum returns the sum of the resource versions of the
// secrets and configmaps the function references. Shared by the newdeploy and
// container managers.
//
// We used to update a timestamp in the deployment environment field in order to
// trigger a rolling update when the function's referenced resources changed.
// But a timestamp also changes when the executor merely adopts an orphaned
// deployment, triggering an unwanted rolling update. The sum of the referenced
// resources' resource versions reflects content changes without depending on
// time, so adoption alone does not perturb it.
//
// Each reference is fetched by name (rather than listing every secret/configmap
// in the namespace) to minimise API traffic. Behaviour is preserved: only
// references resolvable within the deployment namespace contribute, and a
// missing referenced object contributes nothing rather than erroring.
func ReferencedResourcesRVSum(ctx context.Context, client kubernetes.Interface, namespace string, secrets []fv1.SecretReference, cfgmaps []fv1.ConfigMapReference) (int, error) {
	rvCount := 0

	for _, ref := range secrets {
		if ref.Namespace != namespace {
			continue
		}
		s, err := client.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return 0, err
		}
		rv, _ := strconv.ParseInt(s.ResourceVersion, 10, 64)
		rvCount += int(rv)
	}

	for _, ref := range cfgmaps {
		if ref.Namespace != namespace {
			continue
		}
		cfg, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return 0, err
		}
		rv, _ := strconv.ParseInt(cfg.ResourceVersion, 10, 64)
		rvCount += int(rv)
	}

	return rvCount, nil
}
