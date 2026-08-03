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

## State schema (statestore)

The statestore schema follows **expand/contract**, and the reason is the skew rule above: during a rolling upgrade a control-plane component at N−1 keeps reading a schema an N component has already migrated.

- Schema change is **additive only** within a release: new tables and new nullable (or defaulted) columns.
- Removal, renames, and in-place type changes happen **a release later**, once no supported line still reads the old shape.
- A migration that has shipped is **never edited** — append a new one.
  A cluster that already applied it will never re-run it, so an edit reaches only fresh installs and the two schemas diverge permanently, with nothing reporting the difference.

These are enforced, not just documented: `pkg/statestore/sqlstore` fails the build on destructive DDL, on a version gap or reorder, and on any change to an already-pinned migration's statements.

## Notable default changes

Behaviour changes that arrive by a changed default, rather than by a new flag, are listed here so an upgrade holds no surprises.

- **`nameFormat` defaults to `standard`.**
The generated names of the chart's hook Jobs are now release-qualified instead of truncated to 24 characters, so two releases whose names share a 24-character prefix no longer collide.
Only hook Jobs are affected — no Deployment, Service, Secret, ConfigMap, ServiceAccount, Role or CRD name derives from this.
Set `nameFormat: legacy` to keep the previous names.
A hook Job that failed and was never cleaned up will linger under its old name after the upgrade; it is inert, but can be deleted by hand.

- **The executor's rollout becomes overlap-free (`maxSurge: 0, maxUnavailable: 1`), and `adoptExistingResources` defaults to `true`.**
Together these are what let poolmgr's warm (specialized) pods survive a `helm upgrade` in place — previously every upgrade deleted them, and requests hung in the cold-start path while the executor and pools rolled (~4% of requests during the roll, measured by the upgrade gate).
The overlap-free rollout exists because the executor is a single-writer control plane keyed on its instance ID: the previous default rolled two executors concurrently with no leader election, and the incoming one's cleanup deleted the objects the outgoing one was still stamping.
It is expressed within `RollingUpdate` rather than as `type: Recreate` because a live Deployment carries a defaulted `rollingUpdate` block that the API forbids alongside `Recreate` — flipping the type fails validation on every existing install.
The cost is a bounded executor-down window per upgrade (≲60s): warm poolmgr and newdeploy traffic keep serving throughout (neither needs a live executor); only cold starts wait for the new one.
Adoption means the executor re-stamps the previous instance's pods and Deployments in place (same UID) instead of recreating them.
Opt-outs: `executor.strategy` and `executor.adoptExistingResources`.
The same default serves HA installs (replicas > 1 with leaderElection): pods roll one at a time and a dying leader releases its lease — never set `maxUnavailable: 0` with leader election, which deadlocks against the leadership-gated readiness probe.
One caveat rides the first upgrade to this release: pods born under the previous release carry no environment-generation label, so an environment updated during that same upgrade window keeps its old pods until the idle reaper or a function update recycles them.

- **`Environment.terminationGracePeriod` defaults to `90` seconds — and now actually defaults.**
A terminating function pod keeps serving for the whole grace window (the drain preStop hook sleeps through it), so this value is exactly how long every pod teardown takes: idle reap, environment update, upgrade, node drain.
Two changes land together.
First, the CRD now carries the default, closing a hole where an Environment created via the API (GitOps, Terraform, `kubectl apply`) without the field got **0** — pods were SIGKILLed instantly and every in-flight request on them died as a connection reset; only the CLI's client-side default masked this.
Second, the default drops from 360 to 90: 90s covers endpoint propagation plus the 60s default function timeout with margin (mirroring the router's own 75s-drain/90s-grace posture), where 360 made every teardown linger six minutes per pod.
Set `terminationGracePeriod` per environment if your functions serve requests longer than ~80s.
This is the **Environment-level** grace only; the container executor's function-level `--graceperiod` keeps its 360s default.
How it reaches existing objects: CRD structural defaults apply when the apiserver **serves** an object, not only at write time — so an Environment stored without the field reads back as 90 the moment the upgraded CRD is applied, with no object update.
Practically, every such environment's pods roll **once** on the first executor reconcile after this upgrade (the pod template's grace changes 0→90); that roll is the fix taking effect.
Only an explicit `terminationGracePeriod: 0` applied as raw YAML keeps 0 — and raw YAML is now the *only* way to express 0: the Go field's `omitempty` drops a zero before the apiserver sees it, so the CLI and any typed client cannot set it (they never could — `omitempty` predates this change — but previously the served value for an absent field was 0, so it looked like it worked).
Making 0 first-class again for typed clients requires migrating the field to a pointer; tracked as follow-up work.

## Deprecation policy

- Flags, CRD fields, chart values, and CLI commands are deprecated with **notice in one full minor release before removal**.
- A deprecation notice names the replacement (or states that there is none) in the release notes and, where possible, in a runtime warning.
- Removal happens no earlier than the first minor after the notice minor.
- CRD schema changes are additive-only within the support window: a single storage version, no conversion webhooks, and field removal only through the deprecation cycle above.

## Where releases are published

- GitHub releases: <https://github.com/fission/fission/releases> (binaries, changelog).
- Helm charts: the `fission-charts` Helm repository.
- Container images: `ghcr.io/fission`.
