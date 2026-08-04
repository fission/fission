# RFC-0028: Zero-downtime upgrades and a predictable release lifecycle

- Status: Implemented and enforced ([#3631](https://github.com/fission/fission/pull/3631) phase 1, [#3636](https://github.com/fission/fission/pull/3636) phase 2, [#3637](https://github.com/fission/fission/pull/3637) phase 3, [#3641](https://github.com/fission/fission/pull/3641)/[#3644](https://github.com/fission/fission/pull/3644) phase 4, [#3642](https://github.com/fission/fission/pull/3642) phase 5, [#3649](https://github.com/fission/fission/pull/3649) follow-ups; gate-closing wave [#3651](https://github.com/fission/fission/pull/3651) instrumentation + grace default, [#3652](https://github.com/fission/fission/pull/3652) warm-pod survival, [#3656](https://github.com/fission/fission/pull/3656) steady-state drain posture in the gate, [#3658](https://github.com/fission/fission/pull/3658) pre-label keep fix, [#3659](https://github.com/fission/fission/pull/3659) absolute service FQDNs; merged 2026-07-31–2026-08-04) — all five phases shipped: the `RELEASES.md` lifecycle policy and the **upgrade-under-load CI leg**; rollout posture (replicas/PDBs, graceful-shutdown wiring, webhook cert stability, overlap-free executor rollout with adoption); version-skew hardening ([#776](https://github.com/fission/fission/issues/776), closed); the state-schema expand/contract guards; and the skip-level (N−2 → N) leg, exercised but deliberately not promised.
  **The gate passes and gates.** From the pre-wave baseline of ~3.6k requests per executor through a ~31s upgrade — poolmgr **146 transport failures (4.0%)** + 6 platform-attributed, newdeploy **32 platform-attributed (0.9%)**, **warm-pod survival 0 of 1** — the measured state on `main` is now **zero on all six failure metrics with warm-pod survival 1, on both the N−1 and N−2 legs** (runs `bae3c6ab`, `a3c9b9c7`). The evaluation step enforces the budget on the supported N−1 → N path and stays observational on skip-level, whose residual failure mode (established connections blackholing at the pre-drain N−2 router's deletion) is unreachable from HEAD and retires as drain-capable releases age into the N−2 slot. This closes [#1856](https://github.com/fission/fission/issues/1856).
- Previous: Implemented, gate not yet passing; Proposed (draft for discussion)
- Tracking issue: [#3625](https://github.com/fission/fission/issues/3625) (anchor: [#1856](https://github.com/fission/fission/issues/1856), open, the highest-reacted issue in the repo; #776 open. #2777, #3245 and #517 are **closed** — cited as evidence of demand for the release-lifecycle policy, not as issues this RFC reopens)
- Supersedes: —
- Targets: policy + CI gate in the next minor; rollout-posture defaults the minor after
- Requires: RFC-0002 EndpointSlice data plane (shipped, default-on) — the property that warm traffic does not depend on a live executor is load-bearing here; RFC-0015 failure attribution (shipped) — lets the upgrade gate distinguish platform-caused errors from user-code errors; `AdoptExistingResources` (shipped, serial-suite-tested); `cmd/preupgradechecks` (shipped Helm hook).

## Summary

Make `helm upgrade fission` a zero-downtime operation **as a tested guarantee**, and publish a release/support policy users can plan around.
The deliverable is threefold: (1) an upgrade-under-load CI gate that fails the build if an upgrade drops platform-attributed requests, (2) rollout choreography — drain, surge, ordering, webhook availability, version-skew rules — that makes the gate pass, and (3) a written release-lifecycle policy (cadence, support window, backport rules, deprecation window) that answers #2777 and #3245 without promising an unstaffable LTS program.
This is retention work: #1856 is the highest-reacted open issue in the repo, and every shipped feature RFC compounds only for users who trust the platform enough to run it.

## Motivation

Nothing in Fission's docs or CI says what happens to in-flight traffic during `helm upgrade`, and #1856's answer from experience is "downtime".
The failure surface, enumerated:

1. **Router rollout gaps** — much is already right: HTTP listeners drain in-flight requests on SIGTERM (`pkg/utils/httpserver` runs `server.Shutdown` under a drain-timeout context), and `/readyz` flips only after the first successful mux build post cache-sync and is already the chart's readinessProbe.
   The endpoint index is also already covered: its informer runs on the manager's cache, the initial materialize is gated on `crMgr.GetCache().WaitForCacheSync`, and no runnable that can flip `ready` starts before the cache group syncs — so a Ready replica does not have a cold index.
   The genuine gaps are narrower than they first look: the endpoint handler *registration*'s `HasSynced` is discarded (`pkg/router/endpointcache/informer.go`), leaving a bounded delivery lag — a one-line fix, not a subsystem; `terminationGracePeriodSeconds` ships unset so the drain window is the platform default; and single-replica remains the default posture.
2. **Webhook unavailability** — the validating/mutating webhook is fail-closed (`failurePolicy: Fail` throughout), hardcoded to `replicas: 1` with no values knob and no PDB template; while it rolls, every Fission CR write errors.
   Worse, on the default cert path (`certManager.enabled=false`, no user PEM) `genCA`/`genSignedCert` mint a **fresh CA at every `helm upgrade` render** (`_certs.tpl` has no `lookup` reuse), so the new `caBundle` in the webhook configs races the still-serving old cert — a fail-closed write outage that replicas and surge cannot fix.
3. **Executor restart** — mid-provision cold starts fail; historically the whole warm map was rebuilt (now mitigated: adopt-in-place keeps pods, and slice-fed routing keeps warm traffic flowing with no executor at all).
4. **Version skew inside one fleet** — #776: builder pods keep the previous fetcher image after upgrade because `ensureBuilder` reconciles on pod *existence* keyed to the Environment's ResourceVersion label (`pkg/buildermgr/environment_reconciler.go`), so a chart-level `FETCHER_IMAGE` change never converges; the same class threatens any control-plane-owned sidecar.
5. **CRDs upgraded out-of-band** — new controllers can race old CRD schemas (RFC-0029 owns the delivery mechanism and its single-writer `crds.mode` design; this RFC owns the ordering contract).
6. **Embedded statestore is a single point on the request path during upgrade** — it is a single-replica Deployment with `strategy: Recreate` on an RWO PVC (required: two pods must never hold the SQLite file), and the router's async enqueue path calls it synchronously; every async enqueue during the Recreate down-window fails.
   Separately, the migration *policy* is better than undocumented — migrations are structurally append-only/additive in `pkg/statestore/sqlstore/migrate.go` — but nothing tests that release N−1 reads release N's schema, so `helm rollback` remains an untested claim.
7. **No cadence** — releases happen when they happen (#3245), and there is no statement of which lines receive fixes (#2777).

Meanwhile the hard building blocks already shipped in the last three waves: adopt-in-place, executor-off-the-hot-path, incremental route reconstruction, drain-on-shutdown, leadership release on cancel, invocation-level failure attribution, and a load harness (RFC-0020) — and an upgrade CI leg (`upgrade_test.yaml`) already installs latest-stable, upgrades to HEAD, and re-tests, though with no load during the roll and in bash that survived the 2026-05 test-suite retirement.
What is missing is choreography and proof under load, not machinery.

## Goals

- A CI leg that installs the latest release, drives continuous mixed load (poolmgr + newdeploy + async + workflow), upgrades to HEAD, and asserts **zero platform-attributed non-2xx** and **warm-pod survival** (pod UIDs stable across the upgrade).
- Default rollout posture in the chart that makes the gate pass: surge-and-drain for router/webhook/executor, readiness gates tied to actual serving ability, PDBs.
- A published **version-skew contract**: which component pairs tolerate N/N−1 during a roll, and the ordering Helm enforces.
- A published **rollback contract**: the conditions under which `helm rollback` to N−1 is safe, enforced by migration policy.
- The #776 class fixed: control-plane-owned images (builder's fetcher sidecar, env builder pods) reconciled on drift, not just existence.
- A release policy document: cadence, support window, backport criteria, deprecation window, and the release gates.

## Non-goals

- A separately-staffed LTS branch program (see Release lifecycle — a firm support window is the honest version of #2777's ask).
- Zero-downtime upgrades of **environment images** (user-owned; generation-based rollout of env pods already exists and RFC-0026's make-before-break covers provisioned floors).
- In-place binary hot-swap, live connection handoff, or anything requiring a service mesh.
- Downgrade support across more than one minor.

## Design

### 1. The gate first: an upgrade-under-load CI leg

Ship the measurement before the fixes, so the current downtime becomes a visible, falsifiable number.

- **Replace, don't duplicate**: the existing `upgrade_test.yaml` leg already does install-latest → fixtures → upgrade-to-HEAD → re-test, but in bash (`test/upgrade_test/fission_objects.sh`, a survivor of the retired bash suite) and with **zero load during the roll** — the exact window this RFC is about.
  Phase 1 rewrites it on the Go framework: kind cluster → `helm install` the **latest published release** (chart + images) → seed fixtures (poolmgr fn, newdeploy fn, an async destination chain, a workflow, an MQ topic pipeline) → start the RFC-0020 bench harness in constant-rate mode (`loadgen/openloop` exists) → `helm upgrade` to HEAD chart + freshly built images → continue load through settle → teardown.
- Named new work in the harness: the load generator records only status and latency today and never reads response headers, so capturing the RFC-0015 attribution header per response is a change in the separate `test/benchmark` Go module, not free.
- Pass criteria, evaluated from the harness log:
  - zero responses whose RFC-0015 attribution marks a Fission component at fault (user-code 5xx from fixtures is impossible by construction; timeout budget generous) — **and zero transport-level failures**: dial errors, connection resets, and client timeouts count as platform-attributed. This second clause is the load-bearing one: the attribution header is written by the router's proxy error handler, so the worst case this RFC exists to prevent — a window with **no Ready router endpoints** — yields connection-refused with no response and no header, and would otherwise score as a pass;
  - poolmgr warm-pod UIDs before == after (adopt-in-place held);
  - async invocations enqueued before the upgrade complete after it (queue durability across the roll);
  - a `WorkflowRun` started before the upgrade reaches `Succeeded` after it (fold resume);
  - builder and env pods report the HEAD fetcher image within one reconcile period (#776 regression test).
- The leg starts **non-gating with a published error budget**, becomes gating once phases 2–3 drive the number to zero, and then becomes a **release gate** (see lifecycle).

### 2. Rollout posture (chart defaults)

Most of this section is **defaults flips of shipped machinery, not new code** — opt-in PDB templates exist for router and executor, `strategy` and `terminationGracePeriodSeconds` values exist with surge documented in comments, SIGTERM drain is implemented in `pkg/utils/httpserver`, and leader-elected components already set `LeaderElectionReleaseOnCancel: true` (crmanager, buildermgr, executor) so lease handoff needs nothing.
The genuinely new pieces are marked.

- **Router**: flip `replicas: 2` default (was the single biggest single-point), `maxSurge: 1 / maxUnavailable: 0`, enable the existing PDB with `minAvailable: 1`, and set a `terminationGracePeriodSeconds` default sized to the request timeout (drain logic already exists; the window is just unset).
  The PDB template renders **only when `replicas ≥ 2` or an autoscaler is enabled** (`autoscaling.enabled` / `keda.enabled`) — a `minAvailable: 1` PDB on a genuinely single-replica Deployment deadlocks node drains, but an HPA/KEDA-scaled router keeps `replicas: 1` in the spec while running more, and must not lose its PDB to a naive guard.
  **New (small)**: honor the endpoint handler registration's `HasSynced` so the bounded delivery lag above is closed; `/readyz` already gates on cache sync and the first mux build, and `/router-healthz` stays a cheap liveness probe, untouched.
- **Webhook** — the one component missing everything (**new**): a `replicas` knob (currently hardcoded 1), a PDB template, surge rollout, and — the actual availability precondition — **cert stability across upgrades**, because today's `_certs.tpl` mints a fresh CA every render and the `caBundle` swap races the still-serving old cert with `failurePolicy: Fail`.
  **cert-manager is the supported path for the zero-downtime guarantee**; Helm `lookup` reuse is documented as plain-Helm best-effort only, since `lookup` is empty under `helm template` (Argo).
  This is one chart-wide policy shared with RFC-0029 §3, not two: **externally-managed material (cert-manager for certs, `internalAuth.existingSecret` for the master) is the supported path on any GitOps render, and the chart fails render rather than minting either one blind** — the two RFCs must land the same guard, and RFC-0029 owns its exact signal.
  Where `lookup` reuse is used it must be **bounded, not unconditional** — but the obvious bound is not expressible: Helm/Sprig has no X.509 parsing, so a template that looks up the existing Secret cannot read `notAfter` or the SAN list out of the PEM. The workable form is to **stamp the facts at mint time** as annotations on the Secret (`fission.io/cert-not-after: <RFC3339>`, `fission.io/cert-sans: <sorted, joined>`) and compare *those* on re-render using Sprig's date helpers plus the recomputed SAN list. Reuse only when remaining validity exceeds 30 days and the SANs match, else regenerate.
  Also shorten the generated lifetime from today's 1825 days, and add an explicit `webhook.regenerateCerts` value (which must invalidate reuse and stay decoupled from `caBundlePEM`) so a suspected-compromise rotation is possible at all — unconditional reuse would happily re-adopt a compromised keypair forever.
  It stays fail-closed (correctness over availability); the gate exercises this with a CR-write loop during the upgrade.
- **Embedded statestore** (**new**): `strategy: Recreate` on the RWO SQLite PVC is correct and stays; the down-window is instead absorbed at the caller — the router's async enqueue path gets a bounded retry-with-backoff budget (within the request timeout) so a Recreate blip delays enqueues rather than failing them; external mode (user Postgres, HA) is documented as the zero-window option.
  Two correctness rules the retry must honor, or it trades an availability blip for duplicate user-function invocations: the HMAC layer has **no anti-replay** (a signature presented twice inside the skew window verifies twice, by documented design) and `Enqueue` deduplicates only on a caller-supplied `X-Fission-Dedup-Key`, which the router does not populate itself.
  So (a) the router **mints its own request-scoped random key before the first attempt** and passes it as the enqueue dedup option — explicitly **not** the invocation id, which is the store-generated message id minted *inside* `Enqueue`, after the dedup probe, and therefore unavailable at the moment it would be needed. On retry the store's dedup probe returns the same id, so the caller still sees a stable `invocationId` and no API changes.
  And (b) each retry is a **freshly built and freshly signed request**. This is sharper than it sounds: the signer reads the body, closes it, replaces it with a `NopCloser` over a consumed reader, and **never sets `GetBody`** — so retrying the same `*http.Request` re-signs an *empty* body which the verifier then happily accepts. The failure mode is silent success with an empty payload, not a 401. The machinery already exists (the statestore client builds requests from a byte slice so net/http populates `GetBody`, and `pkg/utils/httpretry` clones and rewinds per attempt, noting that "the signer below mutates the request it is given"), so the work is *placing* the retry layer above the signer in the router's statestore transport chain with a budget — not building it.
  Honest bounds on what dedup buys, both of which U1 depends on: it collapses a duplicate only while the message is **unsettled** (the dedup key is cleared on settle, so a retry landing after the first copy settled double-invokes — the retry budget must stay well below minimum time-to-settle), and on Postgres it is explicitly best-effort (two truly-concurrent enqueues with the same key can both insert; consumers must still be idempotent). Async delivery remains at-least-once by RFC-0024's design; the goal here is only that an upgrade does not *manufacture* duplicates steady-state operation would not produce.
- **Executor / buildermgr / mqtrigger / timer / canaryconfigmgr**: single-replica is acceptable *because* the data plane no longer depends on them per-request; their requirement is bounded restart, verified by the gate's cold-start-during-upgrade probe rather than by replication.

### 3. Ordering and version skew

Helm applies in one shot, so ordering is expressed through hooks and tolerance rather than sequencing magic:

- **CRDs strictly before controllers** — mechanism owned by RFC-0029's single-writer `crds.mode` design: the `preupgradechecks` hook (today `pre-upgrade` only; gains `pre-install`) **applies** the chart-version CRD bundle via SSA before manifests roll, rather than merely checking — a check-only hook is incoherent, since pre-upgrade hooks run before manifests apply and would inspect the *old* CRDs and fail exactly on CRD-changing upgrades.
- **Skew contract**: during a roll, router@N may coexist with executor@N−1 (and vice versa) — therefore the executor RPC surface and the specialize payload are additive-only within one minor, and any wire change ships behind N-reads-both / N+1-writes-new (the expand/contract discipline).
  The EndpointSlice plane narrows the exposure: warm traffic has **no** cross-version RPC at all.
- **Auth material is wire format and is covered by the same rule** — the omission that would most directly break U1/U3.
  `hmac.KeyVersion` is mixed into the HKDF info string and its own comment says a bump "invalidates every signature in flight; treat it as a wire-format version"; the `Service` identifier set and the per-namespace key-transport encoding are equally load-bearing.
  The building blocks partly exist: the verifier takes a labelled multi-candidate key list, and storagesvc already accepts namespace-derived **and** master-derived keys simultaneously (`ServiceVerifierNamespaceFromHeader`), with a signer-side scheme selector carried on a pod annotation. What is missing is dual-scheme accept on the **sidecar** verifiers, which resolve to exactly one scheme (`VerifierFromKeyOrMaster`).
  Rule, stated symmetrically — a one-sided rule is worse than none: a derivation change ships as **N-accepts-both and still-signs-old**, with N+1 switching the signer, so that a still-running N−1 verifier never sees a signature it cannot check. The derivation version joins what `preupgradechecks`/`fission check` report in §4.
  Two pins for whoever builds the candidate list, because getting them wrong is a silent authz downgrade rather than a visible break: the labelled-candidate hook **replaces** the `Secret`/`OldSecret` pair rather than composing with it, so a dual-derivation verifier must re-include master rotation itself — the candidate set is the cross product `{new, old master} × {v_old, v_new}`, not an independent axis. And each candidate carries the principal namespace recorded on match, where an empty label means *unrestricted principal* — so a compatibility candidate built without its namespace label would silently promote every tenant to unrestricted on the namespace-scoped channels (fetcher, builder, storagesvc) for the whole dual-accept window. Every added candidate carries the same label as its scheme-current counterpart, and **U6 asserts a tenant key still authenticates as that tenant**, not merely that it verifies.
  Note for the gate: `KeyVersion` is a compile-time constant and the CI leg installs the *latest published release* as N−1, so U6 cannot be exercised by "bumping the constant" — it needs a purpose-built N−1 image, which the leg must be able to produce (or U6 is verified by a unit/integration matrix over the verifier's candidate list instead, and the leg asserts only the steady-state no-401 property).
- **Sidecar image drift (#776)**: `ensureBuilder` converges on the *existence* of a builder Service/Deployment labelled with the current Environment ResourceVersion — so a chart-level `FETCHER_IMAGE` change never converges, and (contrary to the obvious guess) deleting a builder pod does not help either: the Deployment simply recreates it from the stale spec. The only triggers today are an Environment RV bump or deleting the builder Deployment. The fix is to reconcile the builder Deployment's images against the chart, rolling it under drain.
- **Pod-ownership reality, which U2/U5 depend on**: generic (`managed=true`) pool pods **are** Deployment-owned, and on every executor restart the pool Deployment is re-Updated because the instanceID annotation differs — so they roll and get **new UIDs** carrying the new fetcher image. Only **specialized** (`managed=false`) pods escape the Deployment and survive an upgrade with the same UID.
  U2's warm-survival guarantee therefore applies to *specialized* pods, and U5's image convergence to Deployment-owned pods; the two do not overlap, which is what makes them jointly satisfiable.
  A second U5 exception must be named rather than discovered: per-image (RFC-0012 Path B) pool Deployments are adopted by an **annotation patch only**, never a spec refresh, so their images do not converge today — either that adoption grows a spec refresh as part of the #776 work, or Path B pools are excluded from U5 explicitly.

### 4. State and rollback contract

- **Migration policy**: the additive-only discipline already exists and is structurally enforced (`pkg/statestore/sqlstore/migrate.go`: versioned, append-only, "DDL is additive only"); what this RFC adds is the *contract on top* — expand-migrate-contract across two minors (destructive contraction only in N+1), a conformance test that runs release N−1's driver against release N's schema, and the user-facing statement that `helm rollback` to N−1 is therefore safe within the support window.
- **CRD policy**: single storage version, additive-only fields within the support window, no conversion webhooks (matches the CEL-limits reality already documented); field removal requires a deprecation cycle (below).
- `fission check` and `preupgradechecks` both learn to report client/server/CRD/statestore-schema versions and flag unsupported skew, so "is this cluster upgradable/rollbackable" is a command, not archaeology.

### 5. Release lifecycle policy (what #2777 / #3245 / #517 were asking for, all now closed)

A `RELEASES.md` (and website page) stating:

- **Cadence**: a minor every quarter (calendar-driven, scope-flexible); patches on demand.
- **Support window**: the latest two minors receive fixes; **security fixes are backported to both**; older lines get nothing.
  This is the honest form of the LTS ask — a small project keeping a firm two-line promise beats a broken three-year one, and the statement itself is what #2777 actually needs.
- **Release gates**: upgrade-under-load leg green (from N−1 *and*, once supported, N−2 skip-level), benchmark suite within regression bars, license/codegen/lint green — mechanical, listed, auditable (#517's remainder).
- **Deprecation policy**: flags, fields, and chart values get a deprecation notice for one full minor before removal; removal notes name the replacement.

## Backward compatibility

Every change in this RFC must be adoptable by an existing install with `helm upgrade` and no manual steps; where a default flips, the old posture stays reachable through values.

- **Chart default flips are opt-out, never removals.** `router.replicas`, PDB enablement, `strategy`, and `terminationGracePeriodSeconds` all remain plain values; small-footprint installs (kind, single-node, the `fission lite` profile discussion in #3040) set `replicas: 1` and get exactly today's behavior — including no PDB, per the render guard above.
  Users who already pinned any of these values see no change at all (flips touch defaults, not overrides).
- **No new hard dependencies.** cert-manager is *required only for the zero-downtime CR-write guarantee*; without it the webhook works exactly as today (with the documented cert-race window during upgrades).
  The external statestore remains optional; embedded mode keeps working with the retry-budget mitigation.
- **Probe semantics are additive.** `/router-healthz` is untouched; `/readyz` gains a condition only under the default data-plane mode; no probe path or port changes, so custom probe configs keep working.
- **Async API contract unchanged.** The enqueue retry budget changes failure timing (delayed instead of fast-fail during a statestore blip), not the surface: same `202 {invocationId}`, same headers, still bounded by the request timeout.
- **#776 fix changes builder pod lifecycle, deliberately.** Builder Deployments now roll on chart image change — previously they silently kept stale images, which is the bug; in-flight builds are safe because pending Packages are re-queued by the reconciler after the roll.
  Warm/specialized function pods are exempt (see §3), so no user-visible warm-capacity loss.
- **The skew contract binds us, additively.** Executor RPC surfaces, the specialize payload, and CRD schemas are additive-only within a minor (expand/contract across two); this is a constraint the RFC *imposes on future changes*, not a change to existing wire formats.
- **Release policy applies prospectively.** The deprecation window (one full minor with notice) governs removals from this point forward; nothing currently shipped is deprecated by this RFC.
- **CI leg replacement is invisible to users.** The bash `upgrade_test.yaml` leg is replaced in-repo; no user-facing surface.

## Rollout phases (bisectable)

1. Rewrite the existing bash `upgrade_test.yaml` leg on the Go framework with load-during-upgrade + attribution scoring (incl. the loadgen response-header capture in `test/benchmark`), non-gating — makes the problem measurable; plus the `RELEASES.md` policy doc (pure writing, immediately answers #2777/#3245).
2. Chart rollout posture: flip router replicas/PDB/strategy/grace defaults (templates exist); build the webhook's missing surface (replicas knob, PDB, cert stability via `lookup`/cert-manager); add endpoint-index sync to router `/readyz`; router async-enqueue retry budget over the statestore Recreate window; gate flips to enforcing for the router/webhook criteria.
3. Skew hardening: #776 image-drift reconciliation (builder Deployments; pool pods exempt), `preupgradechecks`/`fission check` skew reporting, skew contract doc.
4. State contract: the expand/contract policy on top of the existing additive-only migrations + the N−1-driver-vs-N-schema conformance test; rollback documented and tested (a rollback step added to the CI leg).
5. Skip-level (N−2 → N) upgrade support in the same leg; close the epic.

## Invariants & verification

- **U1** — during `helm upgrade` under load, zero platform-attributed failures: no response whose RFC-0015 attribution names a Fission component, **and** no dial error, connection reset, or client timeout. Async enqueues may be *delayed* across the embedded statestore's Recreate window but never failed; duplicates are bounded by the dedup key rather than eliminated — async is at-least-once by RFC-0024's design, so the assertion is that the upgrade produces **no more duplicates than a steady-state run of the same load**, not strict equality. Scoped to the embedded SQLite store, which is what CI runs — on Postgres the dedup collapse is best-effort by design and this clause is advisory. External-statestore installs have no Recreate window at all. CI-enforced (phase 2+).
- **U2** — **specialized** poolmgr pods (`managed=false`, not Deployment-owned) survive a control-plane upgrade with the same UID; provisioned floors (RFC-0026) re-establish within one reconcile. Generic pool pods are explicitly out of scope — they are Deployment-owned and *expected* to roll with new UIDs, which is what delivers U5. CI-enforced.
- **U3** — Fission CR writes succeed at every instant of the upgrade: the webhook never has zero Ready endpoints **and** the serving cert/`caBundle` pair stays coherent (the cert-stability work in §2 is a precondition, not a nice-to-have). CI-enforced by a write loop.
- **U4** — `helm rollback` to N−1 within the support window yields a functioning cluster, including statestore reads. CI-enforced (phase 4).
- **U5** — every control-plane Deployment-owned pod (router, executor, webhook, buildermgr, builder Deployments, storagesvc, statestore, mqtrigger, timer, and generic pool Deployments) converges to the chart's images within one reconcile period of upgrade, without waiting for pod churn. Two exemptions, both named rather than discovered: **specialized** function pods (not Deployment-owned; converge on natural recycle — this is what makes U2 and U5 jointly satisfiable), and RFC-0012 Path B per-image pool Deployments unless their annotation-only adoption grows a spec refresh as part of the #776 work. CI-enforced (#776 test).
- **U6** — no internal-auth channel 401s at any instant of a rolling upgrade, including one that changes a derivation input. Verified by a unit/integration matrix over the verifier's candidate-key list (a compile-time `KeyVersion` cannot be varied by the CI leg, which installs the latest *published* release as N−1); the leg itself asserts the steady-state no-401 property.
- Timing-sensitive pieces (drain deadlines, retry budgets) get `testing/synctest` unit coverage; the ordering/skew rules get a matrix test in the serial integration suite (old executor + new router and inverse, via image pinning).

## Open questions

- Gate scoring: is "zero platform-attributed errors" reachable on kind's noisy CI runners, or does the bar need an explicit tiny budget (e.g. ≤ 1 error per 10k) with flake quarantine like the c500 harness uses?
- Does the two-minor support window start now, or after the first calendar-cadence release proves sustainable?
- Router default `replicas: 2` raises the minimum footprint — acceptable for the default profile, or should the tiny-install profile (`fission lite` discussions, #3040) keep 1 with the downtime trade-off documented?
- Skip-level upgrades (N−2→N): support with the same guarantee, or support with "no guarantee, runs preupgradechecks" as many operators do?
- Cert stability mechanism: Helm `lookup` reuse breaks under `helm template`-based renderers (Argo CD runs `template`, where `lookup` returns empty and would mint a fresh CA every sync) — is the honest answer "cert-manager required for the zero-downtime guarantee, `lookup` as best-effort for plain Helm"?
