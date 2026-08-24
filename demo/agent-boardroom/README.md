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
- `specs/` —
  `fission spec`-format YAML: one shared `node` Environment plus a Package + Function per fixture.
  `support-desk`'s Function carries `spec.agent` (session lifecycle) and `spec.state` (keyed-state keyspace, with `sticky` routing on the session header so the router's real request routing lands on the same pod the dispatcher's `CurrentPod` prediction shows in the UI);
  `order-lookup`'s Function carries `spec.tool`.
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

## Demo beats checklist

Mapped from `06-demo-plan.md`; check these off live against the `/ui/`
boardroom view while `loadgen` runs:

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
- [ ] **Swarm** (not built by this task) — a planner agent fanning out to
      expert sub-agents via async-invoke; tracked separately.
- [ ] **Burst counter** — `loadgen`'s final report line
      (`density (sessions:pods): NNx`) climbs as `--sessions` grows for a
      fixed pool size.

## Success metrics `loadgen` prints

At the end of a run (from `06-demo-plan.md`'s "Success metrics" section —
see that file for the full target numbers):

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
