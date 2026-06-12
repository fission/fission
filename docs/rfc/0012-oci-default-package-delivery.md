# RFC-0012: OCI-Native Package Delivery as the Default Cold-Start Path

- Status: Implemented (phases 1–4; phase 5 docs/UX tracked post-merge)
- Tracking issue: —
- Supersedes: — (builds directly on RFC-0001, shipped in #3484)
- Targets: Fission v1.27+ (phases independently shippable; the default flip and the producer can land in different minors)
- Requires: Kubernetes 1.32 (current floor) for the producer and Path A; Kubernetes 1.33+ (KEP-4639 image volumes) for Path B, auto-detected at executor startup — no Helm version templating needed.

## Summary

RFC-0001 built two OCI delivery paths — fetcher pull (Path A) and kubelet image volumes (Path B) — but left them opt-in for users who bring pre-built images, and left the tarball/storagesvc pipeline as the out-of-the-box default.
This RFC makes OCI the default in three moves:

1. **Consumer flip**: `executor.enableOCIImageVolume` defaults to `true`, so every eligible OCI package rides Path B wherever the cluster supports it (the runtime gate already degrades to Path A below k8s 1.33).
2. **Producer** (the real unlock): a new `packageRegistry` chart block; when configured, every successful build publishes its deployment archive as a digest-pinned single-layer OCI image and the Package is rewritten to `Archive{Type: oci}` — `fission fn create --code` users get OCI delivery with zero per-package action.
   Tarball output remains automatic when no registry is configured.
3. **Per-image idle pool reaper**: the economic enabler that keeps per-(env, image) warm pools from accumulating unboundedly once every built package is its own image.

Path B eliminates the executor→fetcher RPC, the per-pod full-archive download from storagesvc, and the unzip from the cold-start critical path, and gets node-level package caching for free from the kubelet image cache.
The tarball path is **not** deprecated; it remains the fallback and the default for installs without a registry.

## Motivation

The poolmgr cold-start critical path today (fetcher path, verified against main):

1. Router → executor `getServiceForFunction` RPC (`pkg/router/resolver_executor.go:202`).
2. Executor picks a warm pod (`choosePod`) and RPCs the pod's fetcher sidecar (`pkg/executor/executortype/poolmgr/gp_specialize.go:125`).
3. The fetcher downloads the **full deployment archive from storagesvc over HTTP — once per pod, every pod** (`pkg/fetcher/fetcher.go:377-392`; the only cache is the pod's own shared volume).
4. Unzip on the shared volume (`fetcher.go:413-424`), then load via the env's `/v2/specialize`.

Path B (shipped, opt-in) replaces steps 2–4 with a single load-only POST to the env (`pkg/executor/executortype/poolmgr/oci_specialize.go:30-83`): the code is already mounted read-only by the kubelet, digest-pinned, and cached on the node like any image layer.
N pods specializing the same function on one node cost one pull instead of N downloads.

Adoption is structurally limited, though: RFC-0001 deferred the producer ("BuildKit builder + `--oci-push`" — RFC-0001 line 53), so only users who bring pre-built images can use OCI at all.
The common path — `fission fn create --code`, buildermgr builds, `updatePackage` writes a storagesvc URL (`pkg/buildermgr/common.go:166-186`) — never produces an OCI artifact.
"OCI as default" without a producer would be marketing for a minority; this RFC's centerpiece is the producer.

## Goals

- Every built package becomes a digest-pinned OCI artifact when a registry is configured, with no per-package opt-in.
- Every eligible OCI package is delivered via image volume (Path B) by default on capable clusters.
- Warm-pool economics stay bounded at many-packages scale (idle pool reaping).
- Close the largest Path B eligibility gap (functions with Secrets/ConfigMaps).
- Zero behavior change for installs without a registry, and zero regression for tarball functions.

## Non-goals

- Building **runtime images** from Dockerfiles / BuildKit in-cluster — this RFC packages build *output*; no image-building toolchain is added.
- Bundling or operating a registry; `packageRegistry.enabled` stays `false` by default because Fission cannot conjure one.
- CLI-side push for `--deploy file.zip` packages (different credential/trust model — follow-up; see Open questions).
- Registry retention/GC tooling (the storagesvc `archivePruner` has no registry analog; operator's retention policy applies — documented).
- Deprecating storagesvc or the tarball path.
- Artifact signing (cosign); the artifact shape is compatible — follow-up.
- Lazy-loading snapshotters (stargz etc.).

## Design

### Producer: builds publish OCI artifacts

**Who pushes: the builder pod's fetcher**, via an extended `/upload` mode.
buildermgr already delegates artifact handling to the builder-pod fetcher (`fetcherC.Upload(ctx, uploadReq)` at `pkg/buildermgr/common.go:125-140`); the fetcher has the built artifact on its shared volume and already links `pkg/oci` and the kauth keychain.

Wire change (`pkg/fetcher/types.go`):

- `ArchiveUploadRequest` gains `OCIPush *OCIPushSpec{Repository, Tag, PushSecretName, InsecureHosts}`.
- `ArchiveUploadResponse` gains `OCI *fv1.OCIArchive` alongside the existing URL+Checksum fields.

When `OCIPush` is set, the fetcher pushes the deployment **directory** (pre-zip — the upload handler archives only when `ArchivePackage` is true, `common.go:123`) via a new `pkg/oci/push.go`:

```go
// PushDirectory publishes dir as a single-layer OCI image and returns its digest.
func PushDirectory(ctx context.Context, dir, repoTag string, kc authn.Keychain) (digest string, err error)
```

**Artifact shape: a real OCI image manifest** — `empty.Image` + `mutate.AppendLayers` with one tar+gzip layer of the directory contents at the image root, minimal config.
This is deliberately *not* an ORAS artifact manifest: kubelet image volumes and containerd's image store consume image manifests, and `pkg/oci.ExtractImage`'s `mutate.Extract` flattens it — byte-compatible with both consumption paths by construction.

**Package rewrite**: `updatePackage` (`common.go:166-186`) is the single chokepoint that writes the built archive; it writes `Archive{Type: ArchiveTypeOCI, OCI: &OCIArchive{Image, Digest, ImagePullSecrets: [packageRegistry.pullSecret]}}` when the upload response carries OCI.
The digest is always set (returned by the push) — **digest-pinned by default**; the tag (`<repositoryPrefix>/<namespace>/<pkg>:<short-digest>`) is a human affordance only.
Existing rollout machinery (PackageRef.ResourceVersion propagation, fsCache invalidation) is untouched: the Package update is the trigger, exactly as today.

**Push failure**: `packageRegistry.fallbackToStorage` (default `true`) → log + fall back to the storagesvc tarball upload; the build still succeeds and a `OCIPublishDegraded` Package condition surfaces the degradation.
With `false`, the build fails (strict mode for shops requiring OCI provenance).

### Consumer default: flipping Path B on

`executor.enableOCIImageVolume` flips to `true` in `values.yaml` (currently `false` at `values.yaml:235`).
**The chart flips the default; the runtime gate does the degrading**: `ImageVolumeGate` (`pkg/executor/util/imagevolume.go:123-149`) requires both the env flag and k8s ≥ 1.33 (detected once at startup, vendor-suffix tolerant), so a 1.32 cluster with the flag on simply runs Path A — correct and already shipped.
No Helm version templating; the flip is one values line, and `false` is the escape hatch (the old default).

### Path B eligibility closure: the fetcher-retained variant (B-fetcher)

Today `getFunctionOCIArchive` (`pkg/executor/executortype/poolmgr/oci.go:55-87`) excludes functions with Secrets/ConfigMaps from Path B because the fetcher materializes them onto the shared volume (`pkg/fetcher/fetcher.go:439-550`) and Path B pods have no fetcher.

Closure: a second Path B pool variant, **B-fetcher**, which is exactly newdeploy's shipped Path B pattern (`pkg/executor/executortype/newdeploy/newdeploy.go:319-336`):

- The fetcher sidecar stays in the pod, the image volume is mounted at the fetcher's store path, and the fetcher's existing `RootStat(storePath)` early-exit skips the pull.
- The fetcher still does `FetchSecretsAndCfgMaps` and the load — **zero new fetcher code**.
- Specialization routes through the fetcher (`:8000/specialize`) instead of `loadOnlySpecialize`.

The variant is pod-spec-affecting, so it goes into `ociPoolHash` (extend the `parts` slice at `oci.go:31` with a variant marker), preserving RFC-0001's invariant that the pool hash covers every pod-spec-affecting archive field.
Eligibility selection becomes: secrets/configmaps present → B-fetcher; absent → B-direct (today's fetcherless variant).
NetworkPolicy: executor→8000 ingress is already admitted.

Rejected alternatives: native projected volumes (structurally impossible — pool pods exist before function identity, and per-image pools are per-*package* while secrets are per-*function*); secrets relayed via the specialize payload (leaks secret material through the executor and the HTTP path, size limits — rejected on posture).

### Per-image idle pool reaper (prerequisite for the flip)

With the producer on, every built package becomes its own pool: N packages × `poolsize` warm pods, today never shrinking (only specialized *pods* are reaped; empty per-image pool *deployments* live forever).

Spec:

- Each per-image pool (key contains `/`, `poolKey` at `oci.go:39-47`) records `lastActive`, touched at creation and on every specialization routed to it.
- A reaper pass (extension of the poolmgr idle strategy / pool reconciler) deletes a per-image pool when it has **no currently-specialized pods** (checked via the `POOL_OCI_IMAGE_HASH` pod label) AND `now − lastActive > executor.ociPoolIdleReapTime` (new value, default 5m, distinct from the pod idle timeout).
- Generic env pools (`imageHash == ""`) are **never** reaped — exact parity with today.
- Reap = delete the pool deployment + drop the map entry under the pool lock, so a racing `GetFuncSvc` recreates cleanly (on-demand pool creation is already the cold-start path).
- The first cold start after a reap pays pool creation + a kubelet-cached pull — cheap on warm nodes, which is the asymmetry that makes an aggressive window affordable.

### Permanent Path A residents

| Exclusion | Why structural |
|---|---|
| Env v1 | Speaks only the v1 specialize contract with store path `user`; `loadOnlySpecialize` posts `/v2/specialize`. Legacy and frozen. |
| `AllowedFunctionsPerContainerInfinite` | Store path is per-function UID; one shared image mount cannot serve it without per-function pools. These envs already amortize cold starts by design. |
| `KeepArchive` envs (new) | JVM-style envs expect `LoadReq.FilePath` to be an archive **file**; OCI artifacts are directory-shaped and image-volume subPaths must be directories (RFC-0001 amendment). Excluded on both producer (tarball output) and consumer sides. |

All three keep working via Path A / tarball, documented as permanent, not TODOs.

### Path selection (updated decision table)

| Package archive | Cluster | Function | Delivery |
|---|---|---|---|
| url/literal (tarball) | any | any | fetcher download (unchanged) |
| oci | <1.33 or flag off | any | Path A: fetcher OCI pull |
| oci | ≥1.33, flag on | no secrets/cfgmaps, env v2, non-Infinite, non-KeepArchive | **Path B-direct** (no fetcher) |
| oci | ≥1.33, flag on | secrets/cfgmaps present | **Path B-fetcher** (sidecar for secrets only) |
| oci | ≥1.33, flag on | env v1 / Infinite / KeepArchive | Path A |

## Configuration

```yaml
packageRegistry:
  enabled: false                  # producer master switch (no registry = tarball, today's behavior)
  repositoryPrefix: ""            # e.g. ghcr.io/myorg/fission-packages; repo layout <prefix>/<namespace>/<pkg>
  pushSecret: ""                  # dockerconfigjson secret in the builder namespace (write creds)
  pullSecret: ""                  # read-only secret stamped into Archive.OCI.ImagePullSecrets
  insecureHosts: ""               # comma list, reuses FETCHER_ALLOW_INSECURE_REGISTRIES semantics
  fallbackToStorage: true         # push failure -> tarball + OCIPublishDegraded condition; false = fail the build

executor:
  enableOCIImageVolume: true      # FLIPPED (runtime-gated on k8s >= 1.33)
  ociPoolIdleReapTime: 5m         # new: per-image pool idle reap window
```

CLI surface: **no new flags for the default path** — the producer is a server-side decision, which is the feature.
`fission pkg info`/`getdeploy` display the image reference + digest; an escape-hatch annotation `fission.io/package-delivery: tarball` opts a single package out of the producer.
Push and pull credentials are deliberately distinct (least privilege).

## Security

- **Digest pinning by default**: the Package records the pushed digest; kubelet (Path B) and the fetcher (Path A, `pkg/oci/extract.go:72-80`) both verify it — tag mutation in the registry cannot change what runs.
- Registry contents are code-execution-equivalent; the RFC mandates docs guidance on registry RBAC and the `<prefix>/<namespace>/<pkg>` layout for multi-tenant separation.
- Path A keeps the extraction hardening shipped in RFC-0001 (os.Root confinement, symlink/traversal rejection, 2 GiB decompression cap).
- The amendment-15 dichotomy is restated: Path A pulls resolve via cluster DNS under the fetcher allowlist; Path B pulls resolve via the **node** resolver under containerd's trust config — the registry URL must be node-resolvable (see Risks).
- Push secret lives only in the builder namespace; the fetcher resolves it through the existing `pkg/oci.Keychain` (kauth); fetcher RBAC already includes `secrets: get`.

## Latency analysis

| Step | Today (tarball) | Path A (OCI pull) | Path B-direct | Path B-fetcher |
|---|---|---|---|---|
| Router → executor RPC | ✓ | ✓ | ✓ | ✓ |
| choosePod | ✓ | ✓ | ✓ | ✓ |
| Executor → fetcher RPC | ✓ | ✓ | — | ✓ (localhost, no download) |
| Package transfer | full download per pod | registry pull per pod | — (kubelet-cached mount) | — |
| Unzip | ✓ | — (extracted on pull) | — | — |
| Secrets materialization | fetcher | fetcher | n/a (ineligible) | fetcher (k8s API gets only) |
| Env load `/v2/specialize` | ✓ | ✓ | ✓ | ✓ |

What moves off the critical path under Path B: the pull happens at **pool creation** (amortized by the kubelet cache and shared across all functions of the package), not at specialization.
The producer adds push time to **build** wall-clock (not cold start); measured and published as part of Gate B.
First cold start after a pool reap pays pool creation with a warm node cache.

## Failure modes

| Failure | Path | Behavior |
|---|---|---|
| Registry down at build | producer | push fails → tarball fallback + `OCIPublishDegraded` condition (default), or build fails (strict) |
| Registry down at cold start | A | fetcher pull fails → specialize error → executor error/retry — parity with storagesvc-down today |
| Registry down at cold start | B, pool exists | **unaffected** — image already mounted in warm pods (strictly better than today) |
| Registry down at cold start | B, new pool | kubelet ImagePullBackOff → pool ready-wait timeout → cold start errors; kubelet retries, self-heals on recovery; no automatic tarball fallback (post-producer there may be no tarball) — documented |
| Digest mismatch | A / B | fetcher rejects / kubelet refuses the pinned ref — fail closed |
| Image deleted from registry | both | cold starts fail until rebuild; registry retention is the operator's job (explicit non-goal) |
| Artifact > 2 GiB cap | A | fetcher rejects; the producer warns at build time when the artifact exceeds the cap (Path B unaffected — kubelet has no such cap) |
| Executor restart with per-image pools | B | adoption shipped in RFC-0001 (instanceID annotation patch, `adoptPerImagePoolDeployments`) |
| Flag flipped off with live per-image pools | B | pool reconciler cleans up; functions fall back to Path A on next cold start |
| Push-secret rotation | producer | next build picks up the new secret (resolved per build); stale creds → push failure path above |

## Compatibility / upgrade

- All changes are additive; existing url/literal packages and their pools are untouched.
- Existing OCI packages get Path B automatically after the flip; `enableOCIImageVolume: false` restores the old default.
- Migrating an existing built package to OCI = `fission pkg rebuild` after configuring `packageRegistry` (the rebuild re-pushes and rewrites the archive).
- Version floors unchanged: producer and Path A at the 1.32 floor; Path B self-gates at 1.33. When Fission's own floor reaches 1.33 the runtime gate becomes vestigial.

## Rollout phases (each independently shippable)

**Phase 1 — per-image idle pool reaper.**
Touch: `poolmgr/gpm.go` (pool map bookkeeping), idle strategy / pool reconciler, `oci.go`; chart `executor.ociPoolIdleReapTime`.
Tests: reap-eligibility table (active pods present / fresh lastActive / generic pool never reaped); race with concurrent `GetFuncSvc` recreate; integration: Path B pool disappears after idle window and recreates on next request.
Gate: cold-start benchmark unchanged for non-OCI; recreate-after-reap cold start measured and published.

**Phase 2 — B-fetcher variant (secrets/configmaps closure).**
Touch: `oci.go` (variant selection + hash bit), `gp_deployment.go` (fetcher-retained per-image pod spec — copy newdeploy's pattern), `gp.go` (route specialize via fetcher for B-fetcher pools).
Tests: hash includes variant (no pool aliasing); pod-spec invariants for both variants; integration `TestOCIPathBSecrets` (secret-using function rides Path B with the secret materialized).
Gate: B-fetcher cold start within a small bound of B-direct.

**Phase 3 — consumer default flip.**
Touch: `values.yaml` only (gate logic already shipped).
Tests: full integration suite green with the new default on both CI legs (the 1.32-floor leg proves auto-degrade; the 1.36 leg proves Path B); upgrade test (existing generic pools unaffected; pre-existing OCI functions migrate pools on next cold start).
Gate A: published off-vs-on cold-start p50/p95 delta for an OCI package; **zero regression for non-OCI**.

**Phase 4 — producer.**
Touch: `pkg/oci/push.go` (new), `pkg/fetcher/types.go` + Upload handler (push mode + fallback), `pkg/buildermgr/` (registry config plumbing, `updatePackage` OCI write, `OCIPublishDegraded` condition), chart `packageRegistry` block, KeepArchive producer exclusion.
Tests: `PushDirectory` against an in-memory registry (digest round-trip, layer content == dir); Upload-handler mode table; `updatePackage` OCI + fallback writes; integration: source build with registry configured ⇒ digest-pinned `Spec.Deployment.OCI` ⇒ function serves via Path B; registry-down build ⇒ tarball fallback + condition; `pkg rebuild` migrates a tarball package.
Reuses the existing CI localhost-NodePort test registry.
Gates B + C: E2E built-function cold start ≈ pre-built Path B and push overhead published; many-package steady-state warm-pod count bounded by the reaper.

**Phase 5 — docs, UX, migration.**
Quickstart with registry config as the recommended install; migration guide; spec save/apply round-trip for OCI packages; benchmark publication in `rfc/perf-results`; escape-hatch annotation.

## Verification

- Perf harness: `test/benchmark/tests/cold-start` (p50/p95 over 30 sequential cold starts) drives Gates A and B; the scale pattern from `test/benchmark/tests/scale-index` inspires the Gate C many-package scenario.
- All file:line anchors in this RFC verified against main at authoring time.
- CI: integration legs run the default-on configuration after Phase 3; one leg keeps `enableOCIImageVolume=false` pinned to cover Path A until the floor reaches 1.33.

## Alternatives considered

- **storagesvc as an OCI-backed store backend**: adds a fetcher→storagesvc→registry hop for full artifact bytes, conflates the storage API with a registry client, and `/v1/archive` consumers expect download URLs — storagesvc isn't even in the build upload path today.
- **Builder binary pushes**: the builder container is environment-owned (user image); tooling can't be guaranteed there. The fetcher is Fission-owned and injected.
- **buildermgr pushes**: buildermgr never touches artifact bytes today; keeping it control-plane-only preserves the architecture.
- **One Path B variant (always keep the fetcher)**: simpler, but permanently re-adds sidecar memory to every warm pod and re-inserts the fetcher hop for the no-secrets majority.
- **Dual-write tarball+OCI on every build**: doubles storage and publish latency for a fallback that `fallbackToStorage` provides on demand.
- **Bundling a dev registry in the chart**: operational scope creep; CI's NodePort registry recipe is documented instead.

## Open questions

- Tag retention guidance (digest-pinned consumption makes tags cosmetic; how aggressively may operators GC?).
- Per-namespace repositories vs flat layout as the default (`<prefix>/<namespace>/<pkg>` proposed).
- CLI-side push for `--deploy` packages (client credential model) — follow-up RFC or phase 6.
- Should `OCIPublishDegraded` also be surfaced as a Prometheus metric on buildermgr?

## As shipped

Phases 1–4 landed together (per-phase commits on one PR). Deltas from the proposal, driven by implementation and the pre-merge review:

- **`packageRegistry.publishedPrefix`** (new knob): decouples the push endpoint from the recorded consumption reference — the registry split-brain (risk 1) made concrete by CI itself, where builds push via cluster DNS but the kubelet pulls the NodePort name. The digest pins identity across both names.
- **Reap window floor**: the effective per-image idle window is never shorter than the pool's pod-ready timeout + 1m (the proposed 5m default EQUALLED the pod-ready timeout, so an image-pull-bound first cold start could be reaped at the finish line), and choosePod re-touches the activity clock at pod-claim time. Reap-driven destroys are bounded (30s) so an API outage cannot stall the pool actor.
- **Producer observability**: `fission_buildermgr_oci_publish_total{result=published|degraded}` + a buildermgr error log on degradation (the per-package condition alone left a fleet-wide registry outage invisible); `fission_executor_oci_pool_reap_failures_total` keeps the Gate C reap counter honest; `PACKAGE_REGISTRY_ENABLED` parse failures hard-fail startup like the missing prefix.
- **Upload-failure error fidelity**: the fetcher deleted the build artifact even on a failed upload, so the buildermgr client's 5xx retries returned "no such file" and strict-mode push errors were masked; the artifact is now deleted only on success (which also makes the retries meaningful).
- The executor chart unions `packageRegistry.insecureHosts` into the function-pod fetcher allowlist so Path A consumption of produced packages works on <1.33 clusters with insecure registries.
- The v1.36 CI leg runs the producer for the entire builder test corpus (cluster-DNS push, NodePort published prefix) with `TestOCIProducerBuild` asserting the full chain; the v1.34 leg pins image volumes off; v1.32 proves the runtime auto-degrade.

## Risks (top 3, with mitigations)

1. **Registry endpoint split-brain**: the same URL must be reachable/trusted by the fetcher push (cluster DNS, allowlist), fetcher pull (same), and **kubelet** (node resolver, containerd trust).
   A cluster-DNS-only registry makes Path B fail while builds and Path A succeed.
   Mitigation: docs mandate a node-resolvable URL; buildermgr logs a registry-reachability preflight; verify on kind and one managed provider during Phase 4.
2. **Read-only, digest-pinned `/userfunc` at producer scale**: opt-in users self-selected; the producer routes every built function through a read-only mount (`__pycache__`, JVM scratch, envs that write beside code).
   Mitigation: per-supported-environment compatibility matrix early in Phase 4 drives the exclusion lists (this is where KeepArchive came from); exclusions are producer-side too, so incompatible envs simply keep tarballs.
3. **Pool economics fail to close**: N packages × poolsize (×2 for variant splits) bounded only by the idle window; the assumption is kubelet-cached recreation is cheap enough for an aggressive window.
   Gate C falsifies; contingency: per-image pools default to `poolsize=1` (spec'd as the fallback lever).
