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
// Auth (G16 gate LIFTED): the dispatch route this fixture calls is the SAME
// full-privilege POST /agents/{ns}/{name} route any external caller uses,
// gated by JWT_SIGNING_KEY / AGENT_ALLOW_INSECURE (pkg/agentruntime/main.go).
// This pod now carries its OWN per-agent identity: the fetcher writes a
// {namespace, agent, token} credential to the file FISSION_AGENT_TOKEN_PATH
// points at (pkg/fetcher/agenttoken.go) at specialize time, and this fixture
// sends it on every spawn dispatch as Authorization: Bearer <token> plus the
// X-Fission-Agent-Auth-Namespace / X-Fission-Agent-Auth-Name claims headers
// (pkg/agentruntime/identity.go) -- an automatic credential handoff from the
// architect's OWN inbound specialization to its outbound spawn calls, no
// manual provisioning required. See readAgentCredentials()/authHeaders()
// below for the fallback order and the dev-placeholder edge case.
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
// port for any in-cluster caller (see that chart's networkpolicy.yaml). It
// is correct only for install-namespace "fission", which is why the executor
// now injects the real URL as FISSION_AGENT_RUNTIME_URL
// (pkg/executor/util/agentenv.go) -- prefer that; the legacy AGENT_RUNTIME_URL
// manual override and this hardcoded default remain as fallbacks.
const DEFAULT_AGENT_RUNTIME_URL = 'http://agentruntime.fission:8894';
const AGENT_RUNTIME_URL =
  process.env.FISSION_AGENT_RUNTIME_URL || process.env.AGENT_RUNTIME_URL || DEFAULT_AGENT_RUNTIME_URL;
// AGENT_RUNTIME_TOKEN is the pre-G16 manual-provisioning fallback (see
// authHeaders() below): a plain bearer with no claims headers, used only
// when the fetcher-written identity file is absent or carries the dev
// placeholder.
const AGENT_RUNTIME_TOKEN = process.env.AGENT_RUNTIME_TOKEN || '';

const STATE_URL = process.env.FISSION_STATE_URL || '';
const TOKEN_PATH = process.env.FISSION_STATE_TOKEN_PATH || '';

// AGENT_TOKEN_PATH is the G16 identity file the executor points at via
// FISSION_AGENT_TOKEN_PATH (pkg/executor/util/agentenv.go), written by the
// fetcher at specialize time (pkg/fetcher/agenttoken.go). Deliberately a
// SEPARATE env var and file from the state-token pair above: agent identity
// must not require State to be enabled.
const AGENT_TOKEN_PATH = process.env.FISSION_AGENT_TOKEN_PATH || '';

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
  // agentCreds.namespace is a valid fallback here even when its token is the
  // dev placeholder (authHeaders() skips the placeholder for AUTH purposes
  // only) -- the namespace claim itself is always the real one the fetcher
  // wrote, and identity.go's verify() requires the spawn URL's {namespace}
  // to equal the claims header, so falling back to 'default' here on a
  // non-default-namespace secured install would 403 every spawn even with a
  // perfectly valid token.
  return (stateCreds && stateCreds.namespace) || (agentCreds && agentCreds.namespace) || 'default';
}

// DEV_PLACEHOLDER_TOKEN is the fetcher's un-derived stand-in
// (pkg/fetcher/agenttoken.go writeAgentTokenFile): written when the fetcher
// itself has no FISSION_INTERNAL_AUTH_SECRET, so it never verifies as a real
// identity token. Preferring it anyway would matter on one non-default
// install shape: authentication.enabled but internalAuth.enabled=false, where
// the file carries the placeholder and file-first-always would 401 where a
// manually-provisioned AGENT_RUNTIME_TOKEN JWT would have worked -- so
// authHeaders() below falls through to the env token in that case instead.
const DEV_PLACEHOLDER_TOKEN = 'dev-unauthenticated';

// readAgentCredentials mirrors the stateCreds read above (a fetcher-written
// JSON file at specialize time), read once at module load -- not per spawn
// call -- and cached in agentCreds below.
function readAgentCredentials(path) {
  if (!path) {
    return null;
  }
  try {
    return JSON.parse(fs.readFileSync(path, 'utf8'));
  } catch (err) {
    // Log err.code/err.name only: a mid-token corrupt-file parse failure
    // embeds a source snippet in err.message (V8 JSON.parse SyntaxError),
    // and the token is the last field of this JSON -- stringifying err
    // could leak token bytes into pod logs.
    console.error(`architect: could not read agent identity token file ${path}: ${err.code || err.name}`);
    return null;
  }
}

const agentCreds = readAgentCredentials(AGENT_TOKEN_PATH);

// spawnSessionID is the JS twin of pkg/agentruntime/session.go's
// SpawnSessionID: 'c-' + the first 32 lowercase hex characters of
// sha256(parentRef + "/" + stepKey). See the file header comment for the
// cross-language fixed-vector pin this must keep matching.
function spawnSessionID(parentRef, stepKey) {
  const digest = crypto.createHash('sha256').update(parentRef + '/' + stepKey).digest('hex');
  return 'c-' + digest.slice(0, 32);
}

// authHeaders picks the spawn-call credential in preference order:
//  1. The fetcher-written identity file (agentCreds), sent as
//     Authorization: Bearer <token> plus the two claims headers -- but ONLY
//     when its token is not DEV_PLACEHOLDER_TOKEN (see that constant's
//     comment for the install shape this guards).
//  2. AGENT_RUNTIME_TOKEN, sent as a plain bearer with no claims headers
//     (the pre-G16 manual-provisioning fallback).
//  3. No auth -- relies on the cluster's AGENT_ALLOW_INSECURE pass-through.
function authHeaders() {
  if (agentCreds && agentCreds.token && agentCreds.token !== DEV_PLACEHOLDER_TOKEN) {
    return {
      Authorization: `Bearer ${agentCreds.token}`,
      'X-Fission-Agent-Auth-Namespace': agentCreds.namespace,
      'X-Fission-Agent-Auth-Name': agentCreds.agent,
    };
  }
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
