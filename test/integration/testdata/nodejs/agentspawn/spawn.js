// Agent-runtime spawn-parentage fixture (RFC-0031 slice 5 / parent-graph
// plan, Task 5). A minimal cousin of
// demo/agent-boardroom/fixtures/architect.js: on a turn whose JSON body
// carries {"spawn":true}, it derives ONE child session id for stepKey
// "expert-1" and dispatches ONE turn to that child on ITSELF (same
// namespace, same function) with X-Fission-Session set to the derived id and
// X-Fission-Agent-Parent set to this session's own ParentRef. Every turn
// (spawn or not) replies 200 with {"session","parent","spawned"}, where
// "spawned" is the derived child id (or null when this turn did not spawn).
//
// spawnSessionID below is the same byte-identical JS twin of
// pkg/agentruntime/session.go's SpawnSessionID that architect.js carries
// (see that file's header comment for the pinned fixed-vector this must keep
// matching: byte-identical derivation is what TestAgentRuntimeSpawnParentage,
// agentruntime_test.go, actually proves against a real cluster — the Go test
// computes the SAME id via agentruntime.SpawnSessionID and asserts it equals
// both the registry record's key AND this fixture's reported "spawned"
// value).
//
// Unlike architect.js, this fixture never reads a state-token file to learn
// its own namespace: TestAgentRuntimeSpawnParentage already knows its own
// namespace and function name before creating the function, so it passes
// both as plain function env vars (--env-var SELF_NAMESPACE=... --env-var
// SELF_AGENT_NAME=...) instead.
//
// Spawn failures are best-effort and never fail this turn's own reply — see
// architect.js's header comment for the same auth/G16 caveat, which applies
// identically here (this fixture calls the same full-privilege dispatch
// route architect.js does).

'use strict';

const crypto = require('crypto');

const SESSION_HEADER = 'x-fission-session'; // express lower-cases header names
const PARENT_HEADER = 'X-Fission-Agent-Parent';
const STEP_KEY = 'expert-1';

const SELF_NAMESPACE = process.env.SELF_NAMESPACE || 'default';
const SELF_AGENT_NAME = process.env.SELF_AGENT_NAME || '';

// DEFAULT_AGENT_RUNTIME_URL mirrors architect.js's constant of the same
// name: the agentruntime Service's in-cluster DNS name.
const DEFAULT_AGENT_RUNTIME_URL = 'http://agentruntime.fission:8894';
const AGENT_RUNTIME_URL = process.env.AGENT_RUNTIME_URL || DEFAULT_AGENT_RUNTIME_URL;
const AGENT_RUNTIME_TOKEN = process.env.AGENT_RUNTIME_TOKEN || '';

// spawnSessionID is the JS twin of pkg/agentruntime/session.go's
// SpawnSessionID: 'c-' + the first 32 lowercase hex characters of
// sha256(parentRef + "/" + stepKey).
function spawnSessionID(parentRef, stepKey) {
  const digest = crypto.createHash('sha256').update(parentRef + '/' + stepKey).digest('hex');
  return 'c-' + digest.slice(0, 32);
}

function authHeaders() {
  return AGENT_RUNTIME_TOKEN ? { Authorization: `Bearer ${AGENT_RUNTIME_TOKEN}` } : {};
}

// spawnChild dispatches ONE turn to the derived child id on this SAME
// function/namespace, carrying X-Fission-Session (the derived child id) and
// X-Fission-Agent-Parent (parentRef). Best-effort: a 4xx/5xx or network
// error is logged to console.error and reflected in the returned status but
// never thrown -- this turn's own reply is always 200.
async function spawnChild(parentRef, stepKey) {
  const childID = spawnSessionID(parentRef, stepKey);
  const url = `${AGENT_RUNTIME_URL}/agents/${encodeURIComponent(SELF_NAMESPACE)}/${encodeURIComponent(SELF_AGENT_NAME)}`;

  try {
    const res = await fetch(url, {
      method: 'POST',
      headers: Object.assign(
        {
          'Content-Type': 'application/json',
          'X-Fission-Session': childID,
          [PARENT_HEADER]: parentRef,
        },
        authHeaders(),
      ),
      body: JSON.stringify({}),
    });
    if (!res.ok) {
      console.error(`agentspawn: spawn ${stepKey} (${childID}) failed: HTTP ${res.status}`);
      return { childID: childID, ok: false, status: res.status };
    }
    return { childID: childID, ok: true, status: res.status };
  } catch (err) {
    console.error(`agentspawn: spawn ${stepKey} (${childID}) failed: ${err}`);
    return { childID: childID, ok: false, status: 0, error: String(err) };
  }
}

module.exports = async function (context) {
  const req = context.request;
  const sessionID = req.headers[SESSION_HEADER] || 'unknown';
  // Canonical ParentRef form (session.go's ParentRef.String()):
  // "<namespace>/<agent>/<id>".
  const parentRef = `${SELF_NAMESPACE}/${SELF_AGENT_NAME}/${sessionID}`;

  // req.body arrives pre-parsed as an object for a JSON content type in the
  // common case, but support-desk.js (this same testdata family) defends
  // against a raw-string body too -- mirror that defensiveness here rather
  // than assume the parsed-object case always holds.
  let body = req.body;
  if (typeof body === 'string') {
    try {
      body = body.trim() === '' ? {} : JSON.parse(body);
    } catch (err) {
      body = {};
    }
  }
  const shouldSpawn = !!(body && typeof body === 'object' && body.spawn === true);

  // spawned is reported whenever this turn spawned, REGARDLESS of whether
  // the inner dispatch call itself succeeded -- the derivation never fails,
  // only the network call can (see spawnChild's best-effort doc comment).
  let spawned = null;
  if (shouldSpawn) {
    const result = await spawnChild(parentRef, STEP_KEY);
    spawned = result.childID;
  }

  return {
    status: 200,
    body: JSON.stringify({ session: sessionID, parent: parentRef, spawned: spawned }),
    headers: { 'Content-Type': 'application/json' },
  };
};
