// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	apiv1 "k8s.io/api/core/v1"
)

// DrainLifecycle returns a preStop lifecycle that keeps a terminating pod
// alive for the full grace period so the router/endpoints controller can
// drop it from rotation before the process is killed (connection draining,
// see https://github.com/kubernetes/kubernetes/issues/47576#issuecomment-308900172).
//
// It uses the kubelet-native sleep action (GA since Kubernetes 1.30) instead
// of `exec /bin/sleep`: distroless images (e.g. the fetcher) have no sleep
// binary, and an exec'd sleep spanning the whole grace window is always
// SIGKILLed at expiry, failing the hook on every termination.
//
// A non-positive grace returns nil: there is no drain window to hold open,
// so the hook is omitted entirely.
func DrainLifecycle(gracePeriodSeconds int64) *apiv1.Lifecycle {
	if gracePeriodSeconds <= 0 {
		return nil
	}
	return &apiv1.Lifecycle{
		PreStop: &apiv1.LifecycleHandler{
			Sleep: &apiv1.SleepAction{Seconds: gracePeriodSeconds},
		},
	}
}
