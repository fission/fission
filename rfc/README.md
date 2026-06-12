# Fission RFCs

This directory holds architecture RFCs for Fission.
RFCs describe non-trivial, cross-cutting changes to the project — new CRDs, data-plane refactors, backward-compat strategies, release-skewing decisions.
Small bug fixes, dependency bumps, and isolated refactors do **not** need an RFC.

## Index

| #    | Title                                                              | Status   |
|------|--------------------------------------------------------------------|----------|
| 0001 | [OCI-Native Package Delivery](0001-oci-native-package-delivery.md) | Implemented |
| 0002 | [EndpointSlice-Native Data Plane](0002-endpointslice-native-data-plane.md) | Proposed |
| 0003 | [CRD Modernization](0003-crd-modernization.md)                     | Accepted |
| 0004 | [Reconciler Consolidation](0004-reconciler-consolidation.md)       | Implemented |
| 0005 | [SPIFFE Workload Identity](0005-spiffe-workload-identity.md)       | Proposed |
| 0006 | [Runtime Error-Noise Reduction & Pod-Lifecycle Correctness](0006-runtime-error-noise-reduction.md) | Implemented |
| 0007 | [Gateway API Route Provider (deprecate Ingress)](0007-gateway-api-route-provider.md) | Implemented |
| 0008 | [Streaming Invocation Path](0008-streaming-invocation-path.md)      | Implemented |
| 0009 | [Model Artifact Delivery & Caching](0009-model-artifact-delivery.md) | Proposed |
| 0010 | [GPU-Native Inference Functions](0010-gpu-native-inference.md)      | Proposed |
| 0011 | [Functions as MCP Tools & AI Gateway](0011-functions-as-mcp-tools.md) | Accepted |
| 0012 | [OCI-Native Package Delivery as the Default Cold-Start Path](0012-oci-default-package-delivery.md) | Implemented (phases 1–4) |
| 0013 | [Incremental Router Route Updates](0013-incremental-router-routes.md) | Implemented (phases 0–2; phase 3 gated, not built) |
| 0014 | [Router Hot-Path Efficiency](0014-router-hot-path-efficiency.md) | Implemented (#3491) |

## Status values

- **Proposed** — authored and open for feedback; no implementation yet.
- **Accepted** — design agreed; implementation in progress across one or more PRs.
- **Implemented** — all rollout phases have shipped in a released version.
- **Superseded** — replaced by a later RFC; keep for history.
- **Withdrawn** — abandoned without replacement; keep for history.

## Authoring a new RFC

1. Copy the structure of an existing RFC.
   The expected sections are:
   - Summary (what, in two sentences)
   - Motivation (why — problem statement, evidence)
   - Goals / Non-goals
   - Design (the bulk — be specific enough to implement from)
   - Alternatives considered
   - Backward compatibility
   - Rollout phases (incremental, no big-bang merges)
   - Verification / test plan
   - Open questions
2. File it as `rfc/NNNN-<slug>.md` with the next free number, zero-padded to four digits.
3. Open a PR.
   Discussion happens in the PR; updates happen in follow-up commits on the same PR.
4. Merge when consensus is reached.
   The RFC's `Status` starts as `Proposed`; it flips to `Accepted` on merge of the RFC itself and to `Implemented` once every rollout phase in the RFC has shipped.
5. Implementation PRs reference the RFC by number in their description and amend the Rollout section as phases land.

## Prerequisites assumed by current RFCs

- The supported cluster floor is the one enforced in code (`MinimumKubernetesVersion` in `pkg/apis/core/v1/const.go`, currently **1.32**; CI exercises 1.32 / 1.34 / 1.36).
  RFCs may rely on features GA'd at or before that floor (EndpointSlice, CEL validation, Server-Side Apply, native sidecars).
- A feature that needs a **newer** Kubernetes than the floor must be **opt-in and capability-gated**, not a hard bump of the floor.
  For example, RFC-0001's image-volume delivery path (KEP-4639, K8s 1.33+) is opt-in; its fetcher-pull path works on the 1.32 floor.
- Existing public APIs (CRDs, CLI commands, Helm values) must remain backward compatible within a minor release series.
  Deprecations are announced; removals take at least two minor releases.
