'use strict';

// architect is the boardroom demo's "swarm beat" agent function (see
// ../specs/architect.yaml, spec.agent). Like fixtures/support-desk.js it is
// dispatched by fission-bundle --agentPort (pkg/agentruntime/dispatcher.go):
// every call carries the session id in the X-Fission-Session request header,
// and the reply sets X-Fission-Agent-Yield: waiting so the session flips
// back to idle immediately.
//
// What makes this fixture different: on EVERY turn it spawns K_EXPERTS child
// sessions on the support-desk agent, demonstrating session parentage
// end-to-end (RFC-0031 slice 5 / Task 4 of the parent-graph plan) --
// pkg/agentruntime/session.go's ParentRef + SpawnSessionID, dispatcher.go's
// HeaderParent, and the boardroom UI's family tree (Task 4's other half,
// pkg/agentruntime/ui/boardroom.html) all meet here.
//
// Child-id derivation (spawnSessionID below) is a byte-identical JS twin of
// SpawnSessionID (pkg/agentruntime/session.go): same parent + same stepKey
// always derives the same child id, which is what makes re-spawning
// "expert-1".."expert-3" on a LATER turn resume the SAME three child
// sessions rather than minting new ones -- the exactly-once admission row
// the plan calls "demonstrated". The derivation is pinned against Go's own
// fixed-vector test (pkg/agentruntime/session_test.go
// TestSpawnSessionIDFixedVector), reproduced here as a comment so a change
// to the hash, prefix, or truncation length on either side is caught by
// eyeballing this file, not just by CI:
//
//   parent = {namespace:"ns", agent:"a", id:"sess-1"}, stepKey = "step-1"
//   => "c-27304abfe9308ae9849ace27841bc098"
//
// Auth (G16 gate): the dispatch route this fixture calls is the SAME
// full-privilege POST /agents/{ns}/{name} route any external caller uses,
// gated by JWT_SIGNING_KEY / AGENT_ALLOW_INSECURE (pkg/agentruntime/main.go).
// A running function pod holds no per-agent identity of its own today (that
// is the still-open G16 gap the RFC tracks) -- it can only spawn children if
// EITHER the cluster runs the agent runtime with AGENT_ALLOW_INSECURE=true
// (the kind/demo default, see ../README.md's Kind quickstart) OR the
// deployer threads a real dispatch-scoped bearer token into this pod via
// AGENT_RUNTIME_TOKEN. There is no automatic credential handoff from the
// architect's OWN inbound call to its outbound spawn calls.
//
// Spawn failures are best-effort and never fail the architect's own turn: a
// child dispatch returning 4xx/5xx (or a network error) is logged to
// console.error and recorded in the reply body, but the turn still answers
// 200 -- this is a demo fixture illustrating the wire contract, not a
// production supervisor with retry/backoff semantics.

const fs = require('fs');
const crypto = require('crypto');

const SESSION_HEADER = 'x-fission-session'; // express lower-cases header names
const YIELD_HEADER = 'X-Fission-Agent-Yield';

// OWN_AGENT_NAME must match this fixture's own Function name in
// ../specs/architect.yaml -- it is the middle segment of the ParentRef this
// fixture stamps on every child it spawns ("<ns>/architect/<own-session-id>"),
// and the platform has no way to tell this pod its own function name at
// runtime (see the header comment on ownNamespace below for the analogous
// namespace problem).
const OWN_AGENT_NAME = 'architect';
// WORKER_AGENT is the agent every spawned child session runs on -- the
// existing support-desk fixture, unmodified: the brief for this task is
// explicit that workers ARE support-desk sessions, no separate worker
// function needed.
const WORKER_AGENT = 'support-desk';
const K_EXPERTS = 3;

// DEFAULT_AGENT_RUNTIME_URL is the agentruntime Service's in-cluster DNS
// name (charts/fission-all/templates/agentruntime/service.yaml: ClusterIP
// "agentruntime" in the fission namespace, port from
// `fission.agentRuntimePort`, 8894 by default) -- reachable from this pod
// because the agentruntime NetworkPolicy is deliberately permissive on that
// port for any in-cluster caller (see that chart's networkpolicy.yaml).
const DEFAULT_AGENT_RUNTIME_URL = 'http://agentruntime.fission:8894';
const AGENT_RUNTIME_URL = process.env.AGENT_RUNTIME_URL || DEFAULT_AGENT_RUNTIME_URL;
// AGENT_RUNTIME_TOKEN is optional (see the G16 auth note above): absent
// means this fixture relies on the cluster's AGENT_ALLOW_INSECURE dev-mode
// pass-through rather than sending an Authorization header at all.
const AGENT_RUNTIME_TOKEN = process.env.AGENT_RUNTIME_TOKEN || '';

const STATE_URL = process.env.FISSION_STATE_URL || '';
const TOKEN_PATH = process.env.FISSION_STATE_TOKEN_PATH || '';

// stateCreds is read the same way support-desk.js reads it (RFC-0023's
// fetcher-written token file at specialize time), used ONLY to learn this
// pod's own namespace for building the ParentRef below -- a function pod has
// no other reliable way to learn which namespace it was deployed into. See
// ../specs/architect.yaml's spec.state block, which is what makes the
// fetcher actually write this file; without it (or with functionState
// disabled cluster-wide) ownNamespace() falls back to "default", which is
// also where this whole demo's specs deploy everything.
let stateCreds = null;
if (STATE_URL && TOKEN_PATH) {
  try {
    stateCreds = JSON.parse(fs.readFileSync(TOKEN_PATH, 'utf8'));
  } catch (err) {
    console.error(`architect: could not read state token file ${TOKEN_PATH}: ${err}`);
  }
}

function ownNamespace() {
  return (stateCreds && stateCreds.namespace) || 'default';
}

// spawnSessionID is the JS twin of pkg/agentruntime/session.go's
// SpawnSessionID: 'c-' + the first 32 lowercase hex characters of
// sha256(parentRef + "/" + stepKey). See the file header comment for the
// cross-language fixed-vector pin this must keep matching.
function spawnSessionID(parentRef, stepKey) {
  const digest = crypto.createHash('sha256').update(parentRef + '/' + stepKey).digest('hex');
  return 'c-' + digest.slice(0, 32);
}

function authHeaders() {
  return AGENT_RUNTIME_TOKEN ? { Authorization: `Bearer ${AGENT_RUNTIME_TOKEN}` } : {};
}

// spawnOneExpert dispatches one child turn on support-desk for stepKey
// "expert-<k>", carrying X-Fission-Session (the derived child id) and
// X-Fission-Agent-Parent (this architect session's ParentRef). Per
// dispatcher.go's HeaderParent contract, the parent header is consulted ONLY
// the first time this childID is dispatched (session creation); on every
// later turn -- including the identical spawn on the architect's NEXT turn
// -- the same call simply resumes the existing child session.
async function spawnOneExpert(ns, parentRef, ownSessionID, k) {
  const stepKey = `expert-${k}`;
  const childID = spawnSessionID(parentRef, stepKey);
  const url = `${AGENT_RUNTIME_URL}/agents/${encodeURIComponent(ns)}/${encodeURIComponent(WORKER_AGENT)}`;
  const body = JSON.stringify({
    message: `architect ${ownSessionID} dispatching ${stepKey}: triage the current support queue from the ${stepKey} angle.`,
  });

  try {
    const res = await fetch(url, {
      method: 'POST',
      headers: Object.assign(
        {
          'Content-Type': 'application/json',
          'X-Fission-Session': childID,
          'X-Fission-Agent-Parent': parentRef,
        },
        authHeaders(),
      ),
      body: body,
    });
    if (!res.ok) {
      // Best-effort: a 4xx/5xx spawning one child never fails the
      // architect's own turn (see the file header comment).
      console.error(`architect: spawn ${stepKey} (${childID}) failed: HTTP ${res.status}`);
      return { stepKey: stepKey, childID: childID, ok: false, status: res.status };
    }
    return { stepKey: stepKey, childID: childID, ok: true, status: res.status };
  } catch (err) {
    console.error(`architect: spawn ${stepKey} (${childID}) failed: ${err}`);
    return { stepKey: stepKey, childID: childID, ok: false, status: 0, error: String(err) };
  }
}

// spawnExperts fans out all K_EXPERTS spawn calls concurrently -- they are
// independent dispatch calls to independent (deterministically-derived)
// child sessions, so there is no ordering requirement between them, and
// running them in parallel keeps one slow/cold child from serializing the
// architect's own turn latency behind the others.
async function spawnExperts(ns, parentRef, ownSessionID) {
  const tasks = [];
  for (let k = 1; k <= K_EXPERTS; k++) {
    tasks.push(spawnOneExpert(ns, parentRef, ownSessionID, k));
  }
  return Promise.all(tasks);
}

module.exports = async function (context) {
  const req = context.request;
  const sessionID = req.headers[SESSION_HEADER] || 'unknown';
  const ns = ownNamespace();
  // The canonical ParentRef form (session.go's ParentRef.String()):
  // "<namespace>/<agent>/<id>" -- every spawned child's X-Fission-Agent-Parent
  // header carries exactly this string.
  const parentRef = `${ns}/${OWN_AGENT_NAME}/${sessionID}`;

  const experts = await spawnExperts(ns, parentRef, sessionID);

  const reply = {
    session: sessionID,
    parent: parentRef,
    experts: experts,
  };

  return {
    status: 200,
    body: JSON.stringify(reply),
    headers: {
      'Content-Type': 'application/json',
      [YIELD_HEADER]: 'waiting',
    },
  };
};
