# RFD: Next Two Investments by End-User ROI (2026-06)

- Status: Discussion
- Scope: which outstanding RFCs to implement next, ranked by value to existing end users rather than feature buzz.

## Where we are

Shipped recently: RFC-0006 (error-noise/pod-lifecycle), RFC-0007 (Gateway API routes), RFC-0008 (streaming invocation), RFC-0011 Part A (functions as MCP tools, branch pushed).
RFC-0003 and RFC-0004 are mostly landed and the remainder is internal refactoring with no direct user-visible payoff.
Outstanding user-facing RFCs: 0001 (OCI package delivery), 0002 (EndpointSlice data plane), 0005 (SPIFFE identity), 0009 (model artifact cache), 0010 (GPU inference profile).

## The ranking lens

Two things dominate the day-to-day experience of every Fission user, regardless of workload:

1. **Cold-start latency** — the #1 complaint against every FaaS, and the first benchmark anyone runs.
2. **Invocation reliability** — does a request ever 502 for reasons that aren't the user's code (pod churn, executor stalls, stale routes)?

An RFC that moves one of these helps 100% of users on every deploy or every request.
0005/0009/0010 are real but serve a subset: 0005 needs a SPIFFE issuer installed and a security-mature audience; 0009/0010 serve inference users and are explicitly layered on 0001's OCI plumbing.

## Recommendation 1: RFC-0001 — OCI-Native Package Delivery

**User-visible payoff:** cold starts stop paying tarball-fetch + unzip (the dominant fresh-node cost); node-level layer caching and cross-function dedup come free from the registry/kubelet; users get standard tooling for the artifact they actually ship — `crane push`, `cosign` signing, registry RBAC, vulnerability scanning, retention.
Today the deploy artifact is an opaque tarball in `storagesvc` that no ecosystem tool understands.

**Why it beats the alternatives:**

- It is the rare change that is both boring-infrastructure and strategically load-bearing: RFC-0009 (model cache) and RFC-0010 (GPU inference) explicitly consume its pull/keychain/image-volume mechanics.
  Shipping 0001 is also shipping the substrate for the AI roadmap — the ROI compounds.
- Path A (fetcher pulls the OCI image into `/userfunc`) works on the current 1.32 floor with no cluster prerequisites; Path B (image volumes, 1.33+) is an additive optimization.
- Fully opt-in and additive (`Archive.OCI`); zero migration risk for existing users.

**Cost/risk:** moderate.
New field + codegen, fetcher pull path, CLI flag; poolmgr-only in v1.
The risky part (image-volume pre-warm pools) is deferred to Path B and capability-gated.

## Recommendation 2: RFC-0002 — EndpointSlice-Native Data Plane

**User-visible payoff:** this is the "my function randomly 502'd during a deploy" fix.
Concretely:

- Stale pod-IP entries — today the router learns of pod replacement only when a proxy attempt fails and retries; an EndpointSlice informer removes the window where users see retried/failed requests on pod churn or node drain.
- Executor off the hot path — an executor stall today blocks every router cache miss cluster-wide; after 0002 the executor only *creates* capacity.
- Cold-lookup latency drops from an HTTP round-trip (3–10ms+, worse under load) to an informer-cache hit.
- Functions become visible to standard tooling (`kubectl get svc/endpointslices`), where poolmgr pods are invisible today.
- Bundles three known correctness fixes: single-goroutine cache bottleneck, unbounded specialization goroutines, and a request multiplexer that ignores context cancellation.

**Why it beats the alternatives:** nobody will tweet about it, which is exactly the point — it converts Fission's weakest operational property (bespoke router↔executor protocol with known staleness races) into boring Kubernetes-native machinery.
Reliability improvements retain existing users; features attract new ones; retention compounds first.

**Cost/risk:** highest of any outstanding RFC — it rewires the invocation hot path.
Mitigation: it already prescribes incremental phases, and the integration suite plus the RFC-0006 load fixtures give a regression harness that did not exist a year ago.

## Why not the others (now)

- **RFC-0010 / RFC-0009 (GPU + models):** the flashiest pair, and the right *second* wave — but 0009 consumes 0001's mechanics, so doing 0001 first is strictly on the critical path anyway.
  Starting 0010 now means inventing the artifact substrate twice.
- **RFC-0005 (SPIFFE):** a genuine differentiator, but it requires users to operate SPIRE/Istio CA, and the HMAC scheme it augments is already adequate post-GHSA.
  Audience is a minority of the install base today.
- **RFC-0004 remainder:** internal consolidation; do it opportunistically while inside the executor for 0002.

## Proposed sequencing

1. **RFC-0001 Path A** first — smaller blast radius, immediate cold-start + tooling win, unblocks the 0009→0010 chain.
2. **RFC-0002** next, phased behind a flag, leaning on the integration suite for soak.
3. Re-evaluate 0009/0010 once 0001 Path A has a release of production exposure.

## Open question

Whether 0001 Path B (image volumes) should wait for the floor to reach 1.33 naturally or ship capability-gated immediately after Path A — leaning capability-gated, since CI already exercises 1.34/1.36.
