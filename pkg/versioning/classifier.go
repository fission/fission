// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"k8s.io/apimachinery/pkg/api/equality"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// RuntimeAffecting reports whether new differs from old in a way that
// changes what gets specialized into a pod, or is otherwise observable by an
// invocation — the RFC-0025 auto-publish trigger condition (invariant V4).
//
// It is a pure, deterministic function of the two specs: same inputs always
// produce the same answer, and RuntimeAffecting(s, s) is false for every s.
// The auto-publish controller (phase 4 task 2) calls this on every Function
// update; a false negative here means a runtime change that never mints a
// version (an availability bug), so the classification is deliberately
// conservative — fields are placed in the AFFECTING set unless there is a
// clear, documented reason they cannot influence pod contents or an
// invocation-observable outcome.
//
// The comparison is field-by-field (equality.Semantic.DeepEqual per field)
// rather than one whole-spec DeepEqual with exclusions zeroed out first: the
// explicit per-field list below, each with a one-line rationale, is what
// TestRuntimeAffecting_FieldCoverage enforces stays exhaustive as
// FunctionSpec grows, and is the deliverable's documentation value — a
// reviewer can see the classification decision for every field without
// cross-referencing a separate table.
func RuntimeAffecting(old, new fv1.FunctionSpec) bool {
	switch {
	// Environment: selects the runtime image and entrypoint. Changing it
	// always respecializes the pod.
	case !equality.Semantic.DeepEqual(old.Environment, new.Environment):
		return true

	// Package: the deployed code (and, via FunctionName, the entrypoint
	// within a shared package). Always affects pod contents.
	case !equality.Semantic.DeepEqual(old.Package, new.Package):
		return true

	// Secrets: mounted into the specialized pod; a reference change changes
	// pod contents even though the referenced Secret's own data is out of
	// scope here.
	case !equality.Semantic.DeepEqual(old.Secrets, new.Secrets):
		return true

	// ConfigMaps: mounted into the specialized pod, same reasoning as Secrets.
	case !equality.Semantic.DeepEqual(old.ConfigMaps, new.ConfigMaps):
		return true

	// Resources: CPU/memory requests and limits change the pod spec the
	// executor creates.
	case !equality.Semantic.DeepEqual(old.Resources, new.Resources):
		return true

	// InvokeStrategy: executor type and scaling strategy change how (and as
	// what kind of workload) the function is specialized.
	case !equality.Semantic.DeepEqual(old.InvokeStrategy, new.InvokeStrategy):
		return true

	// FunctionTimeout: bounds how long the router waits for a response —
	// invocation-observable (a request that used to succeed can now time out,
	// or vice versa).
	case old.FunctionTimeout != new.FunctionTimeout:
		return true

	// Streaming: switches the router between the buffered and streaming
	// invocation paths — an invocation-observable behavior change.
	case !equality.Semantic.DeepEqual(old.Streaming, new.Streaming):
		return true

	// State: toggles the RFC-0023 keyed-state API and injects a per-function
	// token at specialization time — changes pod contents (env/token) and
	// invocation-observable behavior.
	case !equality.Semantic.DeepEqual(old.State, new.State):
		return true

	// Invocation: tunes async invocation retry/dead-letter/destination
	// behavior — invocation-observable.
	case !equality.Semantic.DeepEqual(old.Invocation, new.Invocation):
		return true

	// Concurrency: caps how many pods may be specialized to serve the
	// function — bounds request admission, which is invocation-observable
	// (requests can start failing/queuing once the cap is hit).
	case old.Concurrency != new.Concurrency:
		return true

	// RequestsPerPod: how many concurrent requests one specialized pod
	// serves — invocation-observable (latency/concurrency behavior changes).
	case old.RequestsPerPod != new.RequestsPerPod:
		return true

	// OnceOnly: whether a specialized pod is torn down after exactly one
	// request — invocation-observable (pod lifetime/reuse behavior changes).
	case old.OnceOnly != new.OnceOnly:
		return true

	// PodSpec: the container-executor pod template — always affects pod
	// contents.
	case !equality.Semantic.DeepEqual(old.PodSpec, new.PodSpec):
		return true

	default:
		return false
	}

	// NOT-AFFECTING fields (intentionally never compared above — this is
	// documentation, not dead code the compiler would otherwise flag, since
	// the switch above already returns in every reachable branch):
	//
	//   - IdleTimeout: only affects when an already-warm pod is reaped: warm-
	//     capacity economics, not a request-observable outcome.
	//   - RetainPods: only affects how many pods are kept after serving
	//     requests: warm-capacity economics.
	//   - ProvisionedConcurrency: only affects pre-warm target / schedule
	//     windows (RFC-0026): warm-capacity economics — changing the pre-warm
	//     target does not change what a request sees once served.
	//   - Tool: MCP catalog metadata (description/schema/tool name/alias);
	//     the MCP registry reads live Functions directly, so this never
	//     drives what gets specialized or how an invocation behaves.
	//   - Versioning: the RFC-0025 recursion guard. The publish snapshot
	//     zeroes this field before it is ever stored (see
	//     normalizedSnapshot/Publish in publish.go), so a Versioning-only
	//     edit publishing itself would recurse forever; it must never be
	//     classified as runtime-affecting.
}
