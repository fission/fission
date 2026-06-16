# Testing Plan — Multi-Namespace Tenancy

**Companion to:** [prd.md](./prd.md)
**Conventions:** `.claude/resources/test-writing-guidelines.md` — testify (`require` for preconditions, `assert` for independent checks), `t.Context()`, fake clientsets over envtest for unit tests, table-driven subtests with `t.Parallel()`.

---

## 1. Strategy

Three layers, matched to where each pillar's risk actually lives:

| Layer | Tooling | Owns |
|---|---|---|
| **Unit** | `go test -race`, fake clientsets, `httptest.Server` | Pure logic: thread-safe resolver, HMAC ns-derivation, membership predicate, CRD validation, controller reconcile (fake client), CLI command wiring. |
| **Integration** | `test/integration/`, build tag `integration`, real kind cluster, testify framework | Cross-process behavior: zero-restart onboarding, RBAC provisioning, end-to-end tenant isolation, offboard/re-onboard. Lives mostly in **`suites/serial/`** because tenant onboarding mutates cluster-wide control-plane state (same reason `AdoptExistingResources` is serial). |
| **Helm/render** | `helm template`/`helm lint`, `make generate-crds` diff check | Chart correctness: release-ns-only master secret, migration Job render, RBAC templates, NetworkPolicy allowlist. |

Security-critical paths get **adversarial** unit tests (a test that actively forges and asserts rejection), not just happy-path round-trips.

---

## 2. Per-phase test matrix

| Phase | New/changed unit tests | New/changed integration tests | Gating CI check |
|---|---|---|---|
| **0** Thread-safe resolver | `namespace_test.go`: concurrent `SetTenants`/`Get` under `-race`; copy-semantics (caller mutation can't corrupt internal map); change-feed fires once per change | — (no behavior change) | `-race` unit pass |
| **1** CRD + controller + CLI | CRD validation (`validation.go` immutability/DNS); `conditions.go` helpers; tenant-controller reconcile on fake client (label→CR materialization, idempotency, conflict matrix, finalizer drain); CLI command parse/print | `serial/tenant_lifecycle_test.go`: `fission tenant enable/list/status/disable` against a live cluster; CR-created and label-created paths both reach `Ready=True` | unit + a minimal serial leg |
| **2** Migration Job | Helm render test (one FissionTenant per `additionalFissionNamespaces`, always incl. `default`); `functionNamespace`→`FissionTenant(default).spec.functionNamespace` mapping | `serial/tenant_no_restart_test.go`: **capture control-plane pod UIDs, add a namespace, assert zero UID change** (the #3298 fix) | the no-restart serial test is a **hard gate** |
| **3** Tier-A cluster-wide + predicate | membership-predicate unit test (non-tenant obj dropped, tenant obj admitted, live-set change re-admits); reconcile-entry guard no-ops non-tenant | `serial/tenant_nontenant_ignored_test.go`: a Function in a non-onboarded ns is never specialized AND workload-create is `Forbidden` | unit + serial |
| **4** Tier-B dynamic cache + RBAC provisioning | per-namespace `cluster.Cluster` add/remove lifecycle (envtest here — needs a real cache/informer); RBAC provisioning shape vs `_function-access-role.tpl` | `serial/tenant_offboard_reonboard_test.go`: onboard→offboard→re-onboard same ns; assert no goroutine leak (pprof goroutine delta) and re-onboard works | serial + goroutine-delta check |
| **5** Per-namespace HMAC keys | `pkg/auth/hmac`: ns round-trip; **master-scoped keys unchanged byte-for-byte** (golden); candidate-key dual-accept; tampered `X-Fission-Auth-Namespace` rejected; empty-master passthrough; the **adversarial attack-walk** | `serial/tenant_isolation_test.go`: two tenants; each invokes own fn; a crafted cross-ns specialize is rejected; neither reads the other's secret | unit (adversarial) + serial |
| **6** `tenancy.mode: cluster` | Helm render: ClusterRoles emitted only in cluster mode; cache options collapse | reuse the common suite under cluster mode in one CI leg (mirrors the RFC-0002 legacy-pin leg) | render + one opt-in leg |
| **7** Archive ns-prefix (follow-up) | storagesvc ID parse/derive; back-compat for bare-UUID archives | archive upload/download isolation between two tenants | unit + serial |

---

## 3. Unit tests — by package

### 3.1 `pkg/utils/namespace.go` (Phase 0 — the enabler)
- `TestNamespaceResolver_ConcurrentReadWrite` — `t.Parallel()`, spawn N goroutines reading `FissionResourceNS()`/`FunctionNamespaces()` while M goroutines call `SetTenants`; must pass `-race`.
- `TestNamespaceResolver_ReturnsCopy` — mutate the returned map/slice; assert internal state unchanged (prevents the gorilla-`Methods()`-style shared-slice aliasing bug called out in CLAUDE.md).
- `TestNamespaceResolver_ChangeFeed` — subscribe, mutate, assert exactly one notification with the correct added/removed diff.
- `TestGetFunctionNS_PerTenantMapping` — extend existing cases so a *non-default* tenant with `FunctionNamespace` set remaps (today only `default` remaps); guard the generalization.

### 3.2 `pkg/auth/hmac/` (Phase 5 — security centerpiece)
- `TestDeriveServiceKeyNS_RoundTrip` — sign with `K(svc, ns)`, verify with the same; cross-ns verify fails.
- `TestMasterScopedKeysUnchanged` — **golden test**: `DeriveServiceKey(master, svc)` output is byte-identical to a checked-in fixture, proving the `info`-string change didn't shift existing keys. Non-negotiable regression guard.
- `TestCandidateKeySet_DualAccept` — a verifier configured with `[nsKey, masterKey]` accepts signatures from both; after dropping `masterKey`, the master-signed request is rejected.
- `TestCanonicalNamespaceSuffix` — the `\n<namespace>` suffix is present **only** when the ns constructor is used; the 4-field canonical form is unchanged for master channels (tamper test).
- `TestStoragesvcNamespaceHeader_Tampered` — flip `X-Fission-Auth-Namespace`, assert 401 (the namespace is bound into the signature).
- `TestEmptyMaster_PassThrough` — empty master ⇒ `VerifierFromKey(nil)`/`SignerFromKey(nil)` short-circuit, identical to today.
- `TestAttackWalk_CrossTenantForge` (**adversarial**) — construct a signer holding only `K(fetcher, "team-a")`; assert a fetcher verifier for `team-b` rejects it, and that no derived-key holder can produce a valid executor/router-internal signature.

### 3.3 `pkg/apis/core/v1` (Phase 1)
- FissionTenant validation: immutable `spec.namespace` (XValidation), DNS-1123 label, required field. Use fake-clientset webhook-style validation where applicable.
- `conditions.go`: the new condition/reason constants resolve via the existing `PrintConditions`/`ConditionStatus` helpers.

### 3.4 Tenant-lifecycle controller reconcile (Phase 1 + 4)
Fake clientset for pure reconcile logic (per conventions: prefer fakes over envtest for unit); envtest only for the Tier-B `cluster.Cluster` add/remove (Phase 4) which needs a real informer.
- Label→CR materialization: labeled Namespace with no CR ⇒ CR created with ownerRef + `managed-by=label`.
- Conflict matrix (the §6.2 table) as table-driven subtests: label removed but CR exists; user CR + unlabeled ns; CR exists but namespace absent.
- Idempotency: reconcile twice ⇒ no duplicate Roles/SAs/Secret (assert via reactor counts, **not** read-back — recall the fake-clientset GetScale/scale-conversion gotcha; assert writes via reactors).
- Finalizer drain: delete CR ⇒ Roles/RoleBindings/SAs/derived-key Secret removed, namespace removed from live set, finalizer released.
- RBAC provisioning shape: generated Roles match `_function-access-role.tpl` rule-for-rule (golden compare) so the dynamic path and the chart path stay in lockstep.

### 3.5 CLI `pkg/fission-cli/cmd/tenant/` (Phase 1)
- Command parse + flag wiring; positional-arg namespace (cluster-scoped CR — bypasses the global `--namespace` resolution).
- `disable` preflight: non-empty namespace without `--force` ⇒ refusal message; with `--force` ⇒ delete call issued.
- Output formatting: `list` table columns; `status` condition block (reuse `util.PrintConditions`).

---

## 4. Integration tests

### 4.1 Framework changes (prerequisite)
`test/integration/framework/namespace.go` currently pins everything to `default` because the router only watches `FISSION_RESOURCE_NAMESPACES`.
Add:
- `framework.EnableTenant(t, ns)` / `framework.DisableTenant(t, ns)` helpers (wrap the CLI or the FissionTenant clientset).
- A `framework.CreateTenantNamespace(t)` that creates a fresh namespace, onboards it, and registers cleanup (mirrors the existing per-test namespace helper but tenant-aware).
- Control-plane pod-UID capture helper `framework.ControlPlanePodUIDs(t)` for the no-restart assertion.
- Goroutine/heap snapshot helper that pulls a pprof profile from the kind-ci observability endpoint (reuse the debug-github-ci tooling) for the offboard/re-onboard leak check.
- The framework's `Router(t)` client and `FISSION_INTERNAL_AUTH_SECRET` signing transport already exist — extend so the per-tenant derived key can be read for the adversarial cross-ns test.

### 4.2 New serial suite (`test/integration/suites/serial/`)
Serial because every test mutates cluster-wide control-plane state (tenant set, RBAC, restarts).
CI runs serial single-package after `common/` reusing the same port-forwards (`go test -tags=integration -p 1 ./test/integration/suites/serial/...`).

- `tenant_lifecycle_test.go` — enable via CLI and via label; both reach `Ready=True`; `list`/`status` reflect reality; `disable --force` drains.
- `tenant_no_restart_test.go` (**the #3298 fix, hard gate**) — capture control-plane pod UIDs; onboard a new namespace; poll for tenant `Ready`; assert **every captured UID is unchanged** and a function in the new namespace is invocable; assert no function in an existing namespace saw a 5xx during the window.
- `tenant_nontenant_ignored_test.go` — create a Function CR in a namespace with no FissionTenant; assert it is never specialized (no pod) and that a direct `kubectl`-equivalent workload create in that ns is `Forbidden` (the RBAC floor).
- `tenant_offboard_reonboard_test.go` — onboard→offboard→re-onboard the same namespace; assert re-onboard works and the goroutine count returns to baseline (Tier-B teardown leak guard).
- `tenant_isolation_test.go` (**security**) — two tenant namespaces, each with a function; assert each invokes its own; using `team-a`'s derived key, attempt a `/specialize` against `team-b`'s fetcher and assert rejection; assert `team-a`'s SA cannot `get` `team-b`'s `fission-internal-auth`.

### 4.3 `common/` suite additions
Most tenant tests are serial, but a few read-only checks fit the parallel `common/` suite:
- A function created in a non-default *already-onboarded* tenant namespace is invocable through `RouterInternalBaseURL()` (validates `UrlForFunction` folding + routing across namespaces under the new watch model).
- `fission tenant list` returns the seeded `default` tenant (smoke).

### 4.4 Known flakes to avoid re-triggering
Per project memory: `TestPoolCacheRequests/scenario-test7` and `TestGoEnv` flake independently of this diff — do **not** chase them; re-run the job.
Avoid asserting live HPA/Deployment reflects a new spec immediately after `fn update` (newdeploy coalesced-specialization is racy) — the tenant tests should assert *invocability*, not live-object field values.

---

## 5. CI wiring (`​.github/workflows/push_pr.yaml`)

- The new serial tenant tests run in the existing serial step (same port-forwards, after `common/`). No new job, just new files picked up by the package glob.
- Add the `--tenantController` pod's `svc:` label to the kind-ci NetworkPolicy allowlist (`charts/fission-all/templates/router/networkpolicy.yaml`) — per the CLAUDE.md gotcha, a missing entry surfaces only in CI as a silent `dial tcp …:8889 i/o timeout`.
- Phase 6 adds one opt-in leg with `tenancy.mode: cluster` (mirrors the existing "Pin legacy data plane" / RFC-0002 legacy-pin step pattern) so the cluster-mode path stays covered.
- `make generate-crds` + `make codegen` diff-check stays green (the FissionTenant CRD YAML must be committed and match the types).
- License header check (`make license-check`) on every new `.go` file.
- The MCP-style fission-namespace log gotcha applies: tenant-controller logs live in the `fission` namespace, which the default `default`-scoped diagnostics dump does NOT capture — pull the `kind-logs` artifact and read `containers/tenantcontroller-*.log` when debugging CI.

---

## 6. Coverage targets

- New `pkg/auth/hmac` ns code: ≥ 90% (security-critical, mostly pure — high coverage is cheap and warranted).
- Tenant-controller reconcile: ≥ 80% via fake-client unit tests; the remaining cross-process behavior is covered by the serial suite (integration coverage counts via the in-process instrumented binary mechanism, per the fission-cli coverage precedent).
- CLI `tenant` group: covered through the integration suite's in-process CLI (the established coverage path — the orphaned `e2e/cli` is not the mechanism).
- Overall: no net coverage regression on the codecov combined report; the no-restart and isolation behaviors are gated by *integration* assertions, not coverage percentage.

---

## 7. Test-authoring checklist (per new test file)

- [ ] testify `require` for preconditions, `assert` for independent checks.
- [ ] `t.Context()`, not `context.Background()`.
- [ ] Table-driven subtests with `t.Parallel()` where independent.
- [ ] Fake clientset for unit; envtest only where a real informer/cache is intrinsic (Tier-B lifecycle).
- [ ] Assert fake-client writes via reactors, not read-back (scale-conversion gotcha).
- [ ] Security tests are adversarial (forge-and-assert-reject), not happy-path only.
- [ ] SPDX license header (`make license`).
- [ ] Integration tests `t.Skip` cleanly when their required runtime image env var is unset.
