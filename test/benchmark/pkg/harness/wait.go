// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"fmt"
	"net/http"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// WaitForPoolReady polls until at least minReady generic runtime pods for the
// environment are Ready, across all namespaces (pool pods may run in a dedicated
// function namespace depending on tenancy config). This lets cold-start
// scenarios wait for a warm pool without specializing it.
func (e *Env) WaitForPoolReady(ctx context.Context, envName string, minReady int, timeout time.Duration) error {
	selector := labels.SelectorFromSet(labels.Set{
		fv1.ENVIRONMENT_NAME: envName,
		"managed":            "true",
	}).String()
	return Poll(ctx, timeout, 2*time.Second, func(ctx context.Context) (bool, error) {
		pods, err := e.Clients.Kube.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err
		}
		return countReady(pods.Items) >= minReady, nil
	})
}

// countReady returns how many of the pods are Running + Ready.
func countReady(pods []apiv1.Pod) int {
	ready := 0
	for i := range pods {
		if podReady(&pods[i]) {
			ready++
		}
	}
	return ready
}

// WaitForRoutable polls the public router for relativeURL until it returns a 2xx,
// which doubles as a warm-up: by the time it succeeds the function is specialized
// and serving. Returns the number of attempts so callers can sanity-check.
func (e *Env) WaitForRoutable(ctx context.Context, relativeURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 30 * time.Second}
	url := e.routerURL + relativeURL
	return Poll(ctx, timeout, 1*time.Second, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, nil // router/pod not up yet; keep polling
		}
		_ = resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 300, nil
	})
}

func podReady(pod *apiv1.Pod) bool {
	if pod.Status.Phase != apiv1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == apiv1.PodReady {
			return c.Status == apiv1.ConditionTrue
		}
	}
	return false
}

// Poll invokes fn every interval until it returns true, ctx is cancelled, or the
// timeout elapses. It is the single polling primitive shared by every wait in
// the suite (pod readiness, routability, package build, cold-start probing).
func Poll(ctx context.Context, timeout, interval time.Duration, fn func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := fn(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
