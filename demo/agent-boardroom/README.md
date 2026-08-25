<!--
SPDX-FileCopyrightText: The Fission Authors
SPDX-License-Identifier: Apache-2.0
-->

# The Fission "boardroom" demo

This directory holds the deployable assets for the agent-runtime boardroom
demo:
Fission's answer to the "actor substrate" density/teleport story
(see `fission/distributed-agent-runtime/06-demo-plan.md` in the sibling
`fission` checkout for the full script and success metrics).
Target claim: **100+ stateful agent sessions multiplexed on <=8 warm pods,
sub-second resume, live UI.**

## Layout

- `fixtures/support-desk.js` —
  the agent function.
  Session-scoped (dispatched by `fission-bundle --agentPort`, see `pkg/agentruntime/dispatcher.go`);
  keeps a per-session turn count and running summary via the RFC-0023 keyed-state API (`FISSION_STATE_URL` + the fetcher-written state token file);
  sets `X-Fission-Agent-Yield: waiting` after every reply so the session flips back to idle immediately.
- `fixtures/order-lookup.js` —
  a trivial MCP tool function (static order data by id).
  Independent of `support-desk` on purpose:
  a real agent would call it mid-conversation,
  but the load generator below drives `support-desk` directly,
  so this stays decoupled from the density/teleport story.
- `fixtures/architect.js` —
  the swarm-beat agent function.
  On every turn it spawns 3 deterministically-derived "expert" child sessions on `support-desk`
  (see `pkg/agentruntime/session.go`'s `SpawnSessionID` / `ParentRef`),
  carrying `X-Fission-Agent-Parent` on each spawn so the platform records real parentage —
  the boardroom UI's family tree (below) renders the result.
  Workers ARE `support-desk` sessions;
  no separate worker function exists.
- `specs/` —
  `fission spec`-format YAML: one shared `node` Environment plus a Package + Function per fixture.
  `support-desk`'s Function carries `spec.agent` (session lifecycle) and `spec.state` (keyed-state keyspace, with `sticky` routing on the session header so the router's real request routing lands on the same pod the dispatcher's `CurrentPod` prediction shows in the UI);
  `order-lookup`'s Function carries `spec.tool`;
  `architect`'s Function carries `spec.agent` (its own session lifecycle) and a minimal `spec.state` (used only so the fetcher writes the state token file `architect.js` reads its own namespace from).
- `loadgen/main.go` —
  a stdlib-only Go CLI that drives sessions against the dispatcher and
  prints the demo's success metrics at the end.

**Relocation note:** `demo/` at the repo top level is a **relocate-at-consolidation candidate** —
`../examples` (the sibling `fission/examples` repo) is the natural home for fixture code once the agent-runtime work ships.
It stays here for now so the whole stack (fixtures, specs, load driver) is self-contained for the end-to-end milestone.

## Kind quickstart

1. Build and deploy with the `kind` skaffold profile
   (the agent runtime is already enabled with `allowInsecure: true` in the
   base `skaffold.yaml` values, so no extra flags are needed for a dev
   cluster):

   ```sh
   kind create cluster --config kind.yaml
   kubectl create ns fission && make create-crds
   SKAFFOLD_PROFILE=kind make skaffold-deploy
   ```

2. Deploy the demo's Environment/Functions via `fission spec apply`
   (run from the repo root so the spec directory's relative `include:`
   globs resolve against `demo/agent-boardroom/`):

   ```sh
   fission spec apply --specdir demo/agent-boardroom/specs
   ```

   There is no cluster-free `fission spec validate` for this repo today —
   `ValidateSubCommand` resolves a live Kubernetes client even for the
   local reference/schema checks, so `spec validate` needs a reachable
   cluster (a `fission-cli/cmd/spec.ReadSpecs` round trip against these
   files, structural-parse only, was used instead during development —
   see the task report).

3. Port-forward the agent runtime service and open the UI:

   ```sh
   kubectl -n fission port-forward svc/agentruntime 8894:8894
   open http://127.0.0.1:8894/ui/
   ```

4. Run the load generator (in another terminal):

   ```sh
   go run ./demo/agent-boardroom/loadgen --sessions 100
   ```

   Useful variations:

   ```sh
   # Fewer, faster sessions for a quick smoke test.
   go run ./demo/agent-boardroom/loadgen --sessions 10 --turns 2 --think 2s-5s

   # Burst/contention beat: hammer turns back to back, no think pause.
   go run ./demo/agent-boardroom/loadgen --sessions 50 --burst

   # Stretch target (GKE-sized cluster, not kind's 8-pod pool).
   go run ./demo/agent-boardroom/loadgen --sessions 250 --think 20s-60s

   # Against an authentication.enabled deployment.
   go run ./demo/agent-boardroom/loadgen --sessions 100 --token "$JWT"
   ```

   Run `go run ./demo/agent-boardroom/loadgen --help` for the full flag
   list (`--agents-url`, `--namespace`, `--agent`, `--environment`,
   `--concurrency`, `--timeout`).

## Swarm beat

`architect` (`fixtures/architect.js` / `specs/architect.yaml`) is a second agent function:
every turn it spawns 3 deterministically-derived "expert" child sessions on `support-desk`,
carrying real parentage (`X-Fission-Agent-Parent`) end to end.
This is manual, not part of `loadgen` —
start one turn by hand and watch the board:

```sh
# Port-forward as in step 3 above, then:
curl -i -X POST http://127.0.0.1:8894/agents/default/architect \
  -H 'Content-Type: application/json' \
  -H 'X-Fission-Session: architect-demo-1' \
  -d '{"message":"kick off the swarm"}'
```

What to watch on the `/ui/` board:

- The response carries `X-Fission-Session: architect-demo-1`
  (echoed back — the dispatcher minted or resumed that exact id)
  and a JSON body listing 3 `experts`, each with its derived `childID` and dispatch status.
- The SESSIONS panel gets a new **root** card for `architect-demo-1`
  (`default/architect`, no depth badge — a root session has none),
  with 3 **child** cards indented underneath it, tree-connected,
  each carrying a `d1` depth badge (`default/support-desk`,
  same as any other support-desk session in every other respect —
  same status chip, same click-to-transcript panel).
- Re-run the exact same `curl` (same `X-Fission-Session: architect-demo-1`):
  the same 3 child ids reappear —
  `SpawnSessionID` derives them from `("default/architect/architect-demo-1", "expert-<k>")`,
  so the SAME turn keeps resuming the SAME 3 children rather than minting new ones each time.
  Watch their `turns` counter climb instead of the child count growing.
- If a child session is later archived (or its agent isn't the one this page fetched)
  while the architect session is still listed,
  its card would show as a root under an `(unknown parent: ...)` group header instead of disappearing —
  the family tree never silently drops a card just because its parent fell out of view.
- Auth note (see `fixtures/architect.js`'s header comment for the full G16 gate writeup):
  on a `kind`/dev deployment (`AGENT_ALLOW_INSECURE=true`, the default per the Kind quickstart above)
  no token is needed for `architect` to spawn children.
  On an `authentication.enabled` install,
  `architect` spawns children with its own injected identity —
  the fetcher writes a per-agent credential to the pod at specialize time,
  and `architect.js` sends it automatically on every spawn dispatch.
  No manual token provisioning is needed;
  `AGENT_RUNTIME_TOKEN` remains a fallback for the one non-default install shape
  (`authentication.enabled` with `internalAuth.enabled=false`)
  where the injected credential is a placeholder.

## Demo beats checklist

Mapped from the boardroom demo script; check these off live against the
`/ui/` boardroom view while `loadgen` runs:

- [ ] **Split view** — SESSIONS panel (registry) next to the POOL panel
      (pods), same layout as the substrate demo's boardroom.
- [ ] **Cheap sessions** — after `loadgen` finishes its first pass, all
      sessions show **idle** and the pool still shows only the warm pods
      from `env-node.yaml`'s `poolsize` (8 on kind) — no snapshots, no
      per-session pod.
- [ ] **Turn anchors** — while `loadgen` is running (or send one turn by
      hand with `curl`), watch a session card flip **active** and draw an
      anchor line to its `currentPod`, then flip back to **idle** once the
      reply's `X-Fission-Agent-Yield: waiting` lands.
- [ ] **Teleport proof** — scale or delete a specialized pod mid-run and
      confirm a session's next turn lands on a different `currentPod`
      while its running summary (turn count intact) carries over:

      ```sh
      kubectl -n default delete pod -l environmentName=node,fission.io/served=true --field-selector=status.phase=Running
      ```
- [ ] **Contention** — run a second `loadgen --burst` pass concurrently
      against a heavier synthetic function to grab pool capacity; confirm
      `support-desk` sessions keep resuming, with 429s visible only past
      the function's `Concurrency` limit (honest backpressure, not
      failure).
- [ ] **Swarm** — start an `architect` session (see "Swarm beat" below) and
      watch its 3 expert children appear indented underneath it in the
      SESSIONS panel, with a `d1` depth badge.
- [ ] **Burst counter** — `loadgen`'s final report line
      (`density (sessions:pods): NNx`) climbs as `--sessions` grows for a
      fixed pool size.

## Observability

The agent runtime and `pkg/mcp` emit OTel traces for the boardroom demo's
request paths — no meters yet (that stays a follow-up; see the observability
plan's Global Constraints).
This section is a reference, not a walkthrough:
the spans exist whether or not a collector is running,
so nothing here is required to run the demo itself.

### What spans exist

- **`invoke_agent <agent>`** —
  one span per agent turn, started in `pkg/agentruntime/dispatcher.go`'s `DispatchTurn`,
  a child of the inbound HTTP request's server span.
  Attributes: `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name`,
  plus `fission.agent.namespace`, `fission.session.id`, `fission.turn`, `fission.turn.source` (`http` or `wake`),
  and on completion `fission.yield` + a span status (`Error` on transport failure, `Ok` otherwise).
  **Wake-stitched:** when a turn yields `continue`, the dispatcher injects its span's trace context
  into the wake envelope it enqueues (`pkg/agentruntime/wake.go`),
  and the woken continuation's `invoke_agent` span is started as a child of THAT context —
  so a whole self-continuation chain (turn, yield, wake, turn, yield, wake, ...)
  is ONE trace end to end, not a new root span per turn.
  A wake delivered without the trace field (e.g. from a pre-upgrade envelope) degrades gracefully
  to a fresh root span, never an error.
- **`execute_tool <tool>`** —
  one span per MCP tool call, started in `pkg/mcp/server.go`'s `callTool`.
  Attributes: `gen_ai.operation.name=execute_tool`, `gen_ai.tool.name`, `fission.agent.namespace` of the target function.
  When the caller's `_meta` carries a valid W3C `traceparent` (MCP 2026-07-28's reserved trace-context keys —
  `traceparent`/`tracestate`/`baggage`), the span parents to that extracted context instead of a fresh one,
  so a caller-supplied trace also stitches through a tool call.
  `_meta` is untrusted input handled additive-only: absent or malformed values never error and never log above `V(1)`.
- **Server spans** on both muxes —
  `fission-agentruntime` (the agent runtime's mux, `/registry/events`, `/ui`, `/healthz`, `/readyz` excluded so SSE and the UI never get spans)
  and `fission-mcp` (the `/mcp` mux, no filter needed).
  These are what `invoke_agent` and `execute_tool` parent to on the inbound side.

Span names and attribute keys follow the OTel **GenAI semantic conventions,
pinned at `go.opentelemetry.io/otel/semconv/v1.37.0`**
(pre-stable/experimental — the spec has moved these keys before; re-verify on any future semconv bump,
see the doc comments next to `pkg/agentruntime/dispatcher.go`'s `tracer()` and `pkg/mcp/tracing.go`).

### Seeing traces locally

Fission bundles no collector or backend — `fission-bundle` only installs the TracerProvider and W3C propagator
(`pkg/utils/otel/provider.go`) and points them at whatever OTLP endpoint you configure.
With no collector reachable, the SDK falls back to `NeverSample`:
trace-ids still propagate end to end (see Task 3's integration test, `TestAgentRuntimeTraceContextPropagation`),
but nothing is exported anywhere, so there's nothing to look at.

The `kind-opentelemetry` skaffold profile points `openTelemetry.otlpCollectorEndpoint` at
`otel-collector.opentelemetry-operator-system.svc:4317`,
but it deploys no collector itself — bring your own via the OpenTelemetry Operator, roughly:

```sh
# 1. Install the operator (cert-manager is its own prerequisite; see the
#    operator's own install docs for that step).
helm upgrade --install opentelemetry-operator open-telemetry/opentelemetry-operator \
  -n opentelemetry-operator-system --create-namespace

# 2. Apply a Collector CR named "otel-collector" in that same namespace, with
#    an otlp receiver on :4317 and whatever exporter you want traces to land
#    on (a "debug" exporter to start, or a real backend's OTLP exporter).
kubectl apply -n opentelemetry-operator-system -f your-otelcollector-cr.yaml
```

Then redeploy with `SKAFFOLD_PROFILE=kind-opentelemetry make skaffold-deploy`
so the agent runtime and mcp pick up the endpoint.

**Reference OTLP backends:**
[Langfuse](https://langfuse.com) and [Arize Phoenix](https://phoenix.arize.com) both accept OTLP directly
and understand the GenAI semconv attributes above out of the box — either is a reasonable target
for the Collector CR's exporter if you want to actually look at the `invoke_agent`/`execute_tool` traces
this demo produces (docs mention only; neither ships with Fission).

## Success metrics `loadgen` prints

At the end of a run (the boardroom demo script's "Success metrics" section
has the full target numbers):

- **Density** — `sessions:pods` ratio, from the run's session count and
  `GET /registry/pool` filtered to `--environment` (target: >=12x on kind,
  stretch 30x+ on a GKE-sized cluster).
- **Turn latency** — p50/p95 across every turn `loadgen` sent, measured
  end-to-end (full response body read, not time-to-first-byte).
- **Serving-pod spread** — distinct `currentPod` values across a final
  `GET /registry/agents/<ns>/<agent>/sessions` snapshot. This is a spread
  count from one end-of-run poll, **not** a true per-session
  resume-on-different-pod tally (that needs a `GET .../sessions/{id}` after
  every turn to see one session's own pod history over time) — `loadgen`
  labels it accordingly rather than overclaiming a teleport count.

Not measured by `loadgen` (would need extra registry surface beyond this
task's scope): the activation-latency breakdown by path
(sticky-warm / rehydrate / cold) and per-session state size.
