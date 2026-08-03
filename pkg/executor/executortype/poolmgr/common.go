// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// envRuntimeHash is a short, stable hash over exactly the Environment inputs
// genDeploymentSpec bakes into the pool pod TEMPLATE — and nothing else.
//
// This, not metadata.generation, is what the pool-roll discriminator and the
// adopt pass key on. Generation moves on EVERY spec write, including ones
// that never touch the template: `env update --poolsize` is a pure replica
// scale (Poolsize feeds only DeploymentSpec.Replicas), and keying on
// generation turned it into a full pool roll that recycled every warm pod —
// under load, since scaling up is what operators do under load. Hashing the
// template inputs means: template unchanged ⇒ hash unchanged ⇒ nothing rolls
// and nothing is recycled, exactly the pre-RFC-0028 behaviour for those
// updates.
//
// json.Marshal is deterministic here (fixed struct field order; map keys are
// sorted), so the value is stable across processes. Changing this function's
// input set in a future release costs at most one warm-pod recycle wave on
// the upgrade that ships the change — document it there if it happens.
func envRuntimeHash(env *fv1.Environment) string {
	in := struct {
		Runtime     fv1.Runtime                `json:"runtime"`
		Resources   apiv1.ResourceRequirements `json:"resources"`
		Grace       int64                      `json:"grace"`
		PullSecret  string                     `json:"pullSecret"`
		ExtNet      bool                       `json:"extNet"`
		Labels      map[string]string          `json:"labels"`
		Annotations map[string]string          `json:"annotations"`
	}{
		Runtime:     env.Spec.Runtime,
		Resources:   env.Spec.Resources,
		Grace:       env.Spec.TerminationGracePeriod,
		PullSecret:  env.Spec.ImagePullSecret,
		ExtNet:      env.Spec.AllowAccessToExternalNetwork,
		Labels:      env.Labels,
		Annotations: env.Annotations,
	}
	b, err := json.Marshal(in)
	if err != nil {
		// Unreachable for these types; an empty hash compares unequal to any
		// stamped value, which degrades to the safe side (recycle).
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// envLabelsMatchLiveEnv reports whether the ENVIRONMENT_UID /
// ENVIRONMENT_RUNTIME_HASH labels on a pool object — a specialized pod, or a
// pool RS's template — still describe the live Environment, i.e. whether the
// runtime it was born with is the one the environment currently specifies.
//
// The adopt pass and the zero-replica-RS cleanup MUST agree on this: adopt
// keeping a pod the RS path would reap (or vice versa) is precisely the split
// that leaks or kills warm pods. One predicate, two callers.
//
// A missing hash label means the object was born under a release that did not
// stamp it — treated as a MATCH, because recycling every pre-label object
// would defeat the upgrade these guards exist to smooth. (The RS path layers
// one extra check on top for that case; see shouldCleanupForRS.)
func envLabelsMatchLiveEnv(labels map[string]string, env *fv1.Environment) bool {
	if uid := labels[fv1.ENVIRONMENT_UID]; uid != "" && uid != string(env.UID) {
		return false
	}
	h, ok := labels[fv1.ENVIRONMENT_RUNTIME_HASH]
	if !ok {
		return true
	}
	return h == envRuntimeHash(env)
}

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
