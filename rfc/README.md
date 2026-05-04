# Fission RFCs

This directory holds architecture RFCs for Fission. RFCs describe
non-trivial, cross-cutting changes to the project — new CRDs, data-plane
refactors, backward-compat strategies, release-skewing decisions. Small
bug fixes, dependency bumps, and isolated refactors do **not** need an
RFC.

## Index

| #    | Title                                                              | Status   |
|------|--------------------------------------------------------------------|----------|
| 0001 | [OCI-Native Package Delivery](0001-oci-native-package-delivery.md) | Proposed |
| 0002 | [EndpointSlice-Native Data Plane](0002-endpointslice-native-data-plane.md) | Proposed |
| 0003 | [CRD Modernization](0003-crd-modernization.md)                     | Proposed |

## Status values

- **Proposed** — authored and open for feedback; no implementation yet.
- **Accepted** — design agreed; implementation in progress across one or
  more PRs.
- **Implemented** — all rollout phases have shipped in a released version.
- **Superseded** — replaced by a later RFC; keep for history.
- **Withdrawn** — abandoned without replacement; keep for history.

## Authoring a new RFC

1. Copy the structure of an existing RFC. The expected sections are:
   - Summary (what, in two sentences)
   - Motivation (why — problem statement, evidence)
   - Goals / Non-goals
   - Design (the bulk — be specific enough to implement from)
   - Alternatives considered
   - Backward compatibility
   - Rollout phases (incremental, no big-bang merges)
   - Verification / test plan
   - Open questions
2. File it as `rfc/NNNN-<slug>.md` with the next free number, zero-padded
   to four digits.
3. Open a PR. Discussion happens in the PR; updates happen in follow-up
   commits on the same PR.
4. Merge when consensus is reached. The RFC's `Status` starts as
   `Proposed`; it flips to `Accepted` on merge of the RFC itself and to
   `Implemented` once every rollout phase in the RFC has shipped.
5. Implementation PRs reference the RFC by number in their description
   and amend the Rollout section as phases land.

## Prerequisites assumed by current RFCs

- **Kubernetes 1.33+** is the minimum supported cluster version. RFCs may
  depend on features GA'd in or before 1.33 (EndpointSlice, CEL
  validation, Server-Side Apply, native sidecars, in-place pod resize,
  OCI volume sources at beta).
- Existing public APIs (CRDs, CLI commands, Helm values) must remain
  backward compatible within a minor release series. Deprecations are
  announced; removals take at least two minor releases.
