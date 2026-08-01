# Fission release lifecycle

This document states how Fission is released, which release lines receive fixes, what gates a release, and how features are deprecated.
It is the policy users can plan upgrades around; the engineering contract behind it (zero-downtime upgrade guarantees, version-skew and rollback rules) is specified in RFC-0028 and lands incrementally behind the upgrade CI gate.

## Cadence

- A **minor release** (`1.N.0`) ships every quarter.
  The date drives the release, not the feature list: work that misses the cut moves to the next minor rather than delaying the release.
- **Patch releases** (`1.N.x`) ship on demand for regressions and security fixes, with no fixed schedule.
- Release candidates are cut from `main` and soak in CI (including the upgrade and benchmark legs) before the release is tagged.

## Support window

- The **latest two minor lines** receive fixes.
  With `1.27` current, that means `1.27` and `1.26`.
- **Security fixes are backported to both supported lines.**
- Bug fixes are backported to the latest line by default, and to the previous line when the defect is severe (data loss, crash loops, broken upgrades).
- Lines older than the window receive nothing, including security fixes; the upgrade path is to move to a supported line.
- There is no separately maintained LTS line.
  A firm two-line promise this project can keep is more useful than a longer one it cannot; if your organization needs a longer window, pin a supported line and plan one upgrade per quarter.

## Release gates

A release is tagged only when all of the following are green, mechanically and auditably:

1. The full CI matrix on the release commit: unit and integration suites across the supported Kubernetes versions, lint, license, codegen drift, and dependency verification.
2. The **upgrade leg**: `helm upgrade` from the previous minor to the release candidate completes with the cluster serving traffic afterwards.
   As RFC-0028 lands, this gate tightens to upgrade-under-load with zero platform-attributed request failures, and later to skip-level (N−2 → N) coverage.
3. The **benchmark suite** within its regression bars against the previous release.
4. No open release-blocking issues on the milestone.

## Version skew during upgrades

- Within one minor, control-plane components tolerate mixed versions while a rolling upgrade is in flight: wire formats and RPC surfaces are additive-only within a minor.
- Formal N/N−1 skew and rollback contracts (including `helm rollback` guarantees) are being specified in RFC-0028 and will be added here as they become tested guarantees rather than intentions.
- **Skip-level upgrades (N−2 → N) are exercised in CI but not promised.**
  Clusters routinely defer upgrading until something forces it, so the jump from an out-of-window line is the common real-world case; CI runs it on every change so we learn whether it works rather than hearing it from an issue.
  The supported path remains one minor at a time.

## Deprecation policy

- Flags, CRD fields, chart values, and CLI commands are deprecated with **notice in one full minor release before removal**.
- A deprecation notice names the replacement (or states that there is none) in the release notes and, where possible, in a runtime warning.
- Removal happens no earlier than the first minor after the notice minor.
- CRD schema changes are additive-only within the support window: a single storage version, no conversion webhooks, and field removal only through the deprecation cycle above.

## Where releases are published

- GitHub releases: <https://github.com/fission/fission/releases> (binaries, changelog).
- Helm charts: the `fission-charts` Helm repository.
- Container images: `ghcr.io/fission`.
