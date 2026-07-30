// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import fv1 "github.com/fission/fission/pkg/apis/core/v1"

func getEnvPoolSize(env *fv1.Environment) int32 {
	var poolsize int32
	if env.Spec.Version < 3 {
		poolsize = 3
	} else {
		poolsize = int32(env.Spec.Poolsize)
	}
	return poolsize
}

func getSpecializedPodLabels(env *fv1.Environment) map[string]string {
	specialPodLabels := make(map[string]string)
	specialPodLabels[fv1.EXECUTOR_TYPE] = string(fv1.ExecutorTypePoolmgr)
	specialPodLabels[fv1.ENVIRONMENT_NAME] = env.Name
	specialPodLabels[fv1.ENVIRONMENT_NAMESPACE] = env.Namespace
	specialPodLabels[fv1.ENVIRONMENT_UID] = string(env.UID)
	specialPodLabels["managed"] = "false"
	return specialPodLabels
}

// copyVersionLabel propagates the fv1.FUNCTION_VERSION label from src to dst
// when present (RFC-0025) -- extracted because every pod/Service label and
// selector site that needs a versioned Function's per-version objects
// distinguishable from its unversioned ones repeats this exact copy. A no-op
// (dst left untouched) when src carries no version label, matching the
// pre-RFC-0025 behaviour of every one of those sites byte-for-byte.
func copyVersionLabel(dst, src map[string]string) {
	if v := src[fv1.FUNCTION_VERSION]; v != "" {
		dst[fv1.FUNCTION_VERSION] = v
	}
}
