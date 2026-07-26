// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// FunctionObjectNames lists the Deployment, Service and HPA objects matching
// selector in ns and returns the union of their names — the shared enumeration
// step of the newdeploy and container managers' cache-INDEPENDENT teardown
// paths (fnDelete and CleanupFunctionVersion). All three kinds are listed (not
// just Deployments) so a partial create that left, say, only a Service behind
// is still found. A per-kind list error is joined into the returned error but
// does not stop the other kinds from contributing names, so the caller can
// delete everything that WAS found and still surface the failure.
func FunctionObjectNames(ctx context.Context, kubernetesClient kubernetes.Interface, ns, selector string) (map[string]struct{}, error) {
	names := make(map[string]struct{})
	var errs error
	opts := metav1.ListOptions{LabelSelector: selector}

	if list, err := kubernetesClient.AppsV1().Deployments(ns).List(ctx, opts); err != nil {
		errs = errors.Join(errs, fmt.Errorf("error listing deployments for teardown: %w", err))
	} else {
		for i := range list.Items {
			names[list.Items[i].Name] = struct{}{}
		}
	}

	if list, err := kubernetesClient.CoreV1().Services(ns).List(ctx, opts); err != nil {
		errs = errors.Join(errs, fmt.Errorf("error listing services for teardown: %w", err))
	} else {
		for i := range list.Items {
			names[list.Items[i].Name] = struct{}{}
		}
	}

	if list, err := kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, opts); err != nil {
		errs = errors.Join(errs, fmt.Errorf("error listing HPAs for teardown: %w", err))
	} else {
		for i := range list.Items {
			names[list.Items[i].Name] = struct{}{}
		}
	}

	return names, errs
}
