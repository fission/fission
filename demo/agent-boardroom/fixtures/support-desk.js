'use strict';

// support-desk is the boardroom demo's agent function (see
// ../specs/function-support-desk.yaml, spec.agent + spec.state). It is
// dispatched by the fission-bundle --agentPort subsystem
// (pkg/agentruntime/dispatcher.go): every call carries the session id in the
// X-Fission-Session request header, and the reply must set
// X-Fission-Agent-Yield: waiting to flip the session back to idle
// immediately rather than waiting out the sweeper's IdleAfter window.
//
// State contract (RFC-0023, see pkg/fetcher/statetoken.go and
// pkg/statesvc/handler.go): the fetcher writes a StateCredentials JSON file
// {namespace, keyspace, token} to FISSION_STATE_TOKEN_PATH at specialize
// time; FISSION_STATE_URL is the statesvc base URL. This fixture keeps the
// per-session KV blob minimal (turn count + a short running summary),
// fetched with GET and written back with PUT.
//
//   GET  {FISSION_STATE_URL}/v1/state/{sessionID}
//   PUT  {FISSION_STATE_URL}/v1/state/{sessionID}
//   Authorization: Bearer <token>
//   X-Fission-State-Namespace: <namespace>
//   X-Fission-State-Keyspace: <keyspace>
//
// On top of that blob (RFC-0027 §G13, see pkg/agentruntime/history.go), this
// fixture also writes REAL per-turn history to the statesvc EventLog and a
// periodic checkpoint to the statesvc KV CAS route, so the boardroom UI's
// session transcript panel has something genuine to render:
//
//   POST {FISSION_STATE_URL}/v1/eventlog/append   {stream,expectedSeq,events}
//   POST {FISSION_STATE_URL}/v1/eventlog/read     {stream,fromSeq,limit}
//   POST {FISSION_STATE_URL}/v1/eventlog/head     {stream}
//   POST {FISSION_STATE_URL}/v1/state/agentckpt.{sessionID}/cas
//        {expectVersion,value}
//
// The history stream name ("agentlog/" + sessionID) and the checkpoint key
// prefix ("agentckpt.") must match pkg/agentruntime/history.go's
// HistoryStreamPrefix / CheckpointKeyPrefix exactly — the platform side reads
// through those same names, never hand-derived twice.
//
// When FISSION_STATE_URL/FISSION_STATE_TOKEN_PATH are unset (functionState
// disabled, or a dev cluster with no statestore) the fixture falls back to
// an in-process Map for the KV blob, and skips history/checkpoint writes
// entirely — that memory does not survive a pod recycle, unlike the real
// state-backed path.
//
// Session workspace beat (G12, see ../README.md's "Session workspace"
// section): on a turn whose body carries {"artifact":true} this fixture
// PUTs a generated few-KiB text artifact to its OWN session workspace at
// "notes/summary.txt" (pkg/agentruntime/workspace.go), GETs it straight
// back, and lists the session, replying with both sha256 hex digests plus
// the list response's items. It authenticates these calls with the SAME
// per-agent identity credential ../fixtures/architect.js's spawn calls use
// (readAgentCredentials()/authHeaders() below, ported from that file
// verbatim) — workspace.go's own-workspace check requires the identical
// (namespace, agent) claims a spawn dispatch already carries, just checked
// more strictly (ns AND agent equality, not ns-only). Best-effort like
// architect.js's spawnOneExpert: a failed PUT/GET/LIST never fails this
// turn's own 200, it only reports "error:<code>" in the corresponding
// field(s) — see artifactBeat's doc comment below.

const fs = require('fs');
const crypto = require('crypto');

const SESSION_HEADER = 'x-fission-session'; // express lower-cases header names
// TURNS_HEADER carries the session's turn count from BEFORE this turn
// (pkg/agentruntime/dispatcher.go's HeaderTurns) — stable across
// at-least-once redeliveries of the SAME turn, which is exactly why it (and
// not a locally-incremented counter) is the replay key for history writes
// below. A direct curl with no dispatcher in front sends no such header;
// parseTurnHeader falls back to 0, so any such calls collide on "turn 0" and
// dedupe against each other — harmless for a demo fixture, not a real bug.
const TURNS_HEADER = 'x-fission-agent-turns';
const YIELD_HEADER = 'X-Fission-Agent-Yield';
const MAX_SUMMARY_CHARS = 240;

// HISTORY_STREAM_PREFIX / CHECKPOINT_KEY_PREFIX mirror
// pkg/agentruntime/history.go's HistoryStreamPrefix / CheckpointKeyPrefix.
const HISTORY_STREAM_PREFIX = 'agentlog/';
const CHECKPOINT_KEY_PREFIX = 'agentckpt.';
// STATE_VERSION_HEADER mirrors stateapi.HeaderVersion — the response header
// GET /v1/state/{key} sets with the value's current KV version, which is how
// this fixture learns the expectVersion for its next checkpoint CAS write
// (the CAS route's 204/412 responses never carry the new version).
const STATE_VERSION_HEADER = 'X-Fission-State-Version';
// CHECKPOINT_EVERY: write a checkpoint on every Nth turn (1-based turn
// number, i.e. turn header value + 1).
const CHECKPOINT_EVERY = 5;
// MAX_APPEND_ATTEMPTS bounds the EventLog CAS-append retry loop: one
// straight-through attempt plus a few resync-and-retry cycles after a 412.
const MAX_APPEND_ATTEMPTS = 4;
// MAX_CHECKPOINT_ATTEMPTS bounds the checkpoint KV CAS retry loop similarly.
const MAX_CHECKPOINT_ATTEMPTS = 2;

// SIMULATED_ORDERS mirrors fixtures/order-lookup.js's static ORDERS table
// (duplicated, not imported: the two fixtures package into separate
// archives/functions and specs/order-lookup.yaml is explicit that the
// loadgen path stays decoupled from an actual order-lookup call). It exists
// only to give the simulated tool_call/tool_result history events a
// realistic-looking payload.
const SIMULATED_ORDERS = [
  { orderId: '1001', item: 'Wireless Mouse', status: 'shipped' },
  { orderId: '1002', item: 'Mechanical Keyboard', status: 'processing' },
  { orderId: '1003', item: 'USB-C Dock', status: 'delivered' },
  { orderId: '1004', item: '4K Monitor', status: 'shipped' },
  { orderId: '1005', item: 'Noise-Cancelling Headphones', status: 'processing' },
];

const STATE_URL = process.env.FISSION_STATE_URL || '';
const TOKEN_PATH = process.env.FISSION_STATE_TOKEN_PATH || '';

let stateCreds = null;
if (STATE_URL && TOKEN_PATH) {
  try {
    stateCreds = JSON.parse(fs.readFileSync(TOKEN_PATH, 'utf8'));
  } catch (err) {
    // err.code/err.name only: a raw JSON.parse SyntaxError embeds a source
    // snippet, and the token is the last field in the file.
    console.error(`support-desk: could not read state token file ${TOKEN_PATH}: ${err.code || err.name}`);
  }
}

// memoryFallback is only ever used when the state API is unreachable; it is
// pod-local and lost on every recycle, so it must never be mistaken for the
// real RFC-0023 keyspace.
const memoryFallback = new Map();

// OWN_AGENT_NAME must match this fixture's own Function name in
// ../specs/support-desk.yaml -- mirrors architect.js's constant of the same
// name; a function pod has no other reliable way to learn its own name at
// runtime.
const OWN_AGENT_NAME = 'support-desk';

// DEFAULT_AGENT_RUNTIME_URL / AGENT_RUNTIME_URL / AGENT_RUNTIME_TOKEN /
// AGENT_TOKEN_PATH / DEV_PLACEHOLDER_TOKEN / readAgentCredentials /
// authHeaders are ported verbatim from ../fixtures/architect.js -- see that
// file's header comment for the full G16 auth writeup this applies
// identically to (the workspace routes are the identity bearer's SECOND
// mount, see pkg/agentruntime/identity.go's package doc).
const DEFAULT_AGENT_RUNTIME_URL = 'http://agentruntime.fission:8894';
const AGENT_RUNTIME_URL =
  process.env.FISSION_AGENT_RUNTIME_URL || process.env.AGENT_RUNTIME_URL || DEFAULT_AGENT_RUNTIME_URL;
// AGENT_RUNTIME_TOKEN is the pre-G16 manual-provisioning fallback (see
// authHeaders() below): a plain bearer with no claims headers, used only
// when the fetcher-written identity file is absent or carries the dev
// placeholder.
const AGENT_RUNTIME_TOKEN = process.env.AGENT_RUNTIME_TOKEN || '';
// AGENT_TOKEN_PATH is the G16 identity file the executor points at via
// FISSION_AGENT_TOKEN_PATH (pkg/executor/util/agentenv.go), written by the
// fetcher at specialize time (pkg/fetcher/agenttoken.go). Deliberately a
// SEPARATE env var and file from the state-token pair above: agent identity
// must not require State to be enabled.
const AGENT_TOKEN_PATH = process.env.FISSION_AGENT_TOKEN_PATH || '';
// DEV_PLACEHOLDER_TOKEN is the fetcher's un-derived stand-in
// (pkg/fetcher/agenttoken.go writeAgentTokenFile): written when the fetcher
// itself has no FISSION_INTERNAL_AUTH_SECRET, so it never verifies as a real
// identity token.
const DEV_PLACEHOLDER_TOKEN = 'dev-unauthenticated';

// readAgentCredentials reads the fetcher-written AgentCredentials JSON
// ({namespace, agent, token}) once at module load, cached in agentCreds
// below.
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
    console.error(`support-desk: could not read agent identity token file ${path}: ${err.code || err.name}`);
    return null;
  }
}

const agentCreds = readAgentCredentials(AGENT_TOKEN_PATH);

// ownNamespace mirrors architect.js's helper of the same name: a function
// pod has no other reliable way to learn which namespace it was deployed
// into. agentCreds.namespace is a valid fallback even when its token is the
// dev placeholder (authHeaders() below skips the placeholder for AUTH
// purposes only) -- the namespace claim itself is always the real one the
// fetcher wrote.
function ownNamespace() {
  return (stateCreds && stateCreds.namespace) || (agentCreds && agentCreds.namespace) || 'default';
}

// authHeaders picks the workspace-call credential in preference order (see
// architect.js's identical helper for the full writeup):
//  1. The fetcher-written identity file (agentCreds) -- but ONLY when its
//     token is not DEV_PLACEHOLDER_TOKEN.
//  2. AGENT_RUNTIME_TOKEN, a plain bearer with no claims headers.
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

// ARTIFACT_PATH is the fixed within-session path the artifact beat
// round-trips (matches the integration test's fixture and assertion, see
// test/integration/testdata/nodejs/agentspawn/spawn.js and
// test/integration/suites/common/agentruntime_test.go's
// TestAgentRuntimeWorkspace).
const ARTIFACT_PATH = 'notes/summary.txt';
// ARTIFACT_MIN_BYTES bounds the generated content to "a few KiB" -- well
// under AGENT_MAX_ARTIFACT_BYTES' 32MiB default.
const ARTIFACT_MIN_BYTES = 3072;

function workspaceURL(ns, agent, sessionID, path) {
  const base = `${AGENT_RUNTIME_URL}/workspace/${encodeURIComponent(ns)}/${encodeURIComponent(agent)}/${encodeURIComponent(sessionID)}`;
  return path ? `${base}/${path}` : base;
}

// buildArtifactContent deterministically generates a few-KiB text artifact
// from the session id and turn number -- no randomness, so a re-run against
// the SAME session/turn produces byte-identical content.
function buildArtifactContent(sessionID, turn) {
  const line = `session ${sessionID} turn ${turn}: session workspace (G12) artifact demo beat -- PUT/GET/LIST round-trip through pkg/agentruntime/workspace.go.\n`;
  let out = '';
  while (out.length < ARTIFACT_MIN_BYTES) {
    out += line;
  }
  return out;
}

// errTag extracts a short, loggable tag from an artifact-beat error: the
// HTTP status code when the call threw a "status:<code>" Error, otherwise
// the error's own code/name -- never its full message.
function errTag(err) {
  const m = /^status:(\d+)$/.exec(err && err.message);
  return m ? m[1] : (err && (err.code || err.name)) || 'network';
}

async function putArtifact(ns, agent, sessionID, content) {
  const res = await fetch(workspaceURL(ns, agent, sessionID, ARTIFACT_PATH), {
    method: 'PUT',
    headers: Object.assign({ 'Content-Type': 'text/plain' }, authHeaders()),
    body: content,
  });
  if (!res.ok) {
    throw new Error(`status:${res.status}`);
  }
  await res.json(); // drain/validate the {path,size} body.
  return crypto.createHash('sha256').update(content, 'utf8').digest('hex');
}

async function getArtifact(ns, agent, sessionID) {
  const res = await fetch(workspaceURL(ns, agent, sessionID, ARTIFACT_PATH), { headers: authHeaders() });
  if (!res.ok) {
    throw new Error(`status:${res.status}`);
  }
  const buf = Buffer.from(await res.arrayBuffer());
  return crypto.createHash('sha256').update(buf).digest('hex');
}

async function listArtifacts(ns, agent, sessionID) {
  const res = await fetch(workspaceURL(ns, agent, sessionID, ''), { headers: authHeaders() });
  if (!res.ok) {
    throw new Error(`status:${res.status}`);
  }
  const data = await res.json();
  return Array.isArray(data.items) ? data.items : [];
}

// artifactBeat runs the PUT -> GET -> LIST sequence and returns the wire
// shape the turn's response echoes: artifactSha256Put/artifactSha256Got are
// the literal string "error:<code>" on failure (never thrown), and
// artifactList is always an array; a LIST failure reports as a single
// sentinel item {path:"error:<code>",size:0} rather than changing the
// field's type.
async function artifactBeat(sessionID, turn) {
  const ns = ownNamespace();
  const content = buildArtifactContent(sessionID, turn);

  let put = 'error:unknown';
  try {
    put = await putArtifact(ns, OWN_AGENT_NAME, sessionID, content);
  } catch (err) {
    console.error(`support-desk: artifact PUT (session ${sessionID}) failed: ${err}`);
    put = `error:${errTag(err)}`;
  }

  let got = 'error:unknown';
  try {
    got = await getArtifact(ns, OWN_AGENT_NAME, sessionID);
  } catch (err) {
    console.error(`support-desk: artifact GET (session ${sessionID}) failed: ${err}`);
    got = `error:${errTag(err)}`;
  }

  let list = [{ path: 'error:unknown', size: 0 }];
  try {
    list = await listArtifacts(ns, OWN_AGENT_NAME, sessionID);
  } catch (err) {
    console.error(`support-desk: artifact LIST (session ${sessionID}) failed: ${err}`);
    list = [{ path: `error:${errTag(err)}`, size: 0 }];
  }

  return { artifactSha256Put: put, artifactSha256Got: got, artifactList: list };
}

function stateHeaders(extra) {
  return Object.assign(
    {
      Authorization: `Bearer ${stateCreds.token}`,
      'X-Fission-State-Namespace': stateCreds.namespace,
      'X-Fission-State-Keyspace': stateCreds.keyspace,
    },
    extra,
  );
}

// nowISO returns the current time as an RFC3339Nano string, the shape
// encoding/json/v2 expects for a Go time.Time field.
function nowISO() {
  return new Date().toISOString();
}

// toBase64 / fromBase64 convert between a UTF-8 string and the base64 the
// wire uses for EventInput.Payload / CASRequest.Value (both Go []byte
// fields).
function toBase64(str) {
  return Buffer.from(str, 'utf8').toString('base64');
}
function fromBase64(b64) {
  return Buffer.from(b64, 'base64').toString('utf8');
}

// parseTurnHeader reads TURNS_HEADER; see its declaration for why an absent
// or unparsable header falls back to 0 rather than throwing.
function parseTurnHeader(raw) {
  const n = parseInt(raw, 10);
  return Number.isFinite(n) && n >= 0 ? n : 0;
}

function historyStream(sessionID) {
  return HISTORY_STREAM_PREFIX + sessionID;
}
function checkpointKey(sessionID) {
  return CHECKPOINT_KEY_PREFIX + sessionID;
}

function simulatedOrder(turn) {
  return SIMULATED_ORDERS[turn % SIMULATED_ORDERS.length];
}

// buildTurnEvents returns the four synthetic events for one turn: turn_start
// (step 0), a simulated order-lookup tool_call/tool_result pair (steps 1-2),
// and yield (step 3, last). Every payload carries the {v,turn,step,kind,at}
// envelope the brief specifies; event-specific detail rides under
// payload.detail so the required keys stay uniform across kinds.
function buildTurnEvents(turn, message) {
  const order = simulatedOrder(turn);
  const specs = [
    { step: 0, kind: 'turn_start', detail: { message: message } },
    { step: 1, kind: 'tool_call', detail: { tool: 'order-lookup', input: { orderId: order.orderId } } },
    { step: 2, kind: 'tool_result', detail: { tool: 'order-lookup', output: order } },
    { step: 3, kind: 'yield', detail: {} },
  ];
  return specs.map(function (s) {
    return {
      type: s.kind,
      payload: { v: 1, turn: turn, step: s.step, kind: s.kind, at: nowISO(), detail: s.detail },
    };
  });
}

async function eventLogHead(sessionID) {
  const res = await fetch(`${STATE_URL}/v1/eventlog/head`, {
    method: 'POST',
    headers: stateHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ stream: historyStream(sessionID) }),
  });
  if (!res.ok) {
    throw new Error(`eventlog head for ${sessionID} failed: ${res.status}`);
  }
  const data = await res.json();
  return data.head;
}

async function eventLogRead(sessionID, fromSeq, limit) {
  const res = await fetch(`${STATE_URL}/v1/eventlog/read`, {
    method: 'POST',
    headers: stateHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ stream: historyStream(sessionID), fromSeq: fromSeq, limit: limit }),
  });
  if (!res.ok) {
    throw new Error(`eventlog read for ${sessionID} failed: ${res.status}`);
  }
  const data = await res.json();
  return data.events || [];
}

// eventLogAppend appends events at expectedSeq. On a 412 CAS conflict it
// returns {conflict:true, head} (head taken from the error envelope, per the
// wire contract's writeEventConflict) instead of throwing, so the caller can
// resync and retry.
async function eventLogAppend(sessionID, expectedSeq, events) {
  const res = await fetch(`${STATE_URL}/v1/eventlog/append`, {
    method: 'POST',
    headers: stateHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({
      stream: historyStream(sessionID),
      expectedSeq: expectedSeq,
      events: events.map(function (e) {
        return { type: e.type, payload: toBase64(JSON.stringify(e.payload)) };
      }),
    }),
  });
  if (res.status === 412) {
    let body = {};
    try {
      body = await res.json();
    } catch (err) {
      // malformed conflict body: fall through with head=null, the caller
      // resyncs with a fresh Head call instead.
    }
    return { conflict: true, head: typeof body.head === 'number' ? body.head : null };
  }
  if (!res.ok) {
    throw new Error(`eventlog append for ${sessionID} failed: ${res.status}`);
  }
  const data = await res.json();
  return { conflict: false, head: data.head };
}

// dropAlreadyWrittenSteps reads the stream's tail at head and filters out any
// of pending's events whose {turn,step} already appears there — the replay
// contract: a redelivered turn (same TURNS_HEADER value) must never
// double-append a step, whether or not the append that actually wrote it
// also happened to answer this pod with a 412.
async function dropAlreadyWrittenSteps(sessionID, turn, pending, head) {
  const tailFrom = Math.max(0, head - (pending.length + 8));
  const tail = await eventLogRead(sessionID, tailFrom, 50);
  const present = new Set();
  for (const ev of tail) {
    try {
      const p = JSON.parse(fromBase64(ev.payload));
      if (p && p.turn === turn && typeof p.step === 'number') {
        present.add(p.step);
      }
    } catch (err) {
      // not a v1 {turn,step} payload this fixture understands (or a
      // different event kind entirely) -- irrelevant to replay dedup.
    }
  }
  return pending.filter(function (e) {
    return !present.has(e.payload.step);
  });
}

// appendTurnHistory CAS-appends this turn's history events, replay-safe
// against both a genuine 412 (concurrent writer) and a clean at-least-once
// redelivery that never conflicts at all (delivery 1 appended and died
// before responding; delivery 2's Head call already reflects delivery 1's
// write, so its append would silently succeed again without the tail-read
// dedup below). Returns the stream's head after this turn's events are
// present (whether this call wrote them or a prior delivery did).
async function appendTurnHistory(sessionID, turn, message) {
  let head = await eventLogHead(sessionID);
  let pending = buildTurnEvents(turn, message);

  for (let attempt = 0; attempt < MAX_APPEND_ATTEMPTS && pending.length > 0; attempt++) {
    if (head > 0) {
      pending = await dropAlreadyWrittenSteps(sessionID, turn, pending, head);
      if (pending.length === 0) {
        break;
      }
    }
    const result = await eventLogAppend(sessionID, head, pending);
    if (!result.conflict) {
      head = result.head;
      pending = [];
      break;
    }
    head = result.head !== null ? result.head : await eventLogHead(sessionID);
    // loop again: the next iteration's dropAlreadyWrittenSteps re-reads the
    // tail at the resynced head and filters pending down to whatever is
    // still missing.
  }

  if (pending.length > 0) {
    console.error(
      `support-desk: session ${sessionID} turn ${turn}: gave up appending ${pending.length} history event(s) after ${MAX_APPEND_ATTEMPTS} attempts`,
    );
  }

  return head;
}

// getCheckpoint reads the current checkpoint record (if any) plus its KV
// version, so a subsequent CAS write knows the right expectVersion. A 404
// (no checkpoint yet) is not an error: version 0 means create-only, matching
// statestore.SetOptions.IfVersion's documented semantics.
async function getCheckpoint(sessionID) {
  const res = await fetch(`${STATE_URL}/v1/state/${encodeURIComponent(checkpointKey(sessionID))}`, {
    headers: stateHeaders(),
  });
  if (res.status === 404) {
    return { version: 0, record: null };
  }
  if (!res.ok) {
    throw new Error(`checkpoint GET for ${sessionID} failed: ${res.status}`);
  }
  const version = parseInt(res.headers.get(STATE_VERSION_HEADER) || '0', 10);
  const record = await res.json();
  return { version: version, record: record };
}

// writeCheckpointCAS attempts one CAS write; returns false (not throws) on a
// 412 conflict so the caller can resync and retry.
async function writeCheckpointCAS(sessionID, record, expectVersion) {
  const res = await fetch(`${STATE_URL}/v1/state/${encodeURIComponent(checkpointKey(sessionID))}/cas`, {
    method: 'POST',
    headers: stateHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ expectVersion: expectVersion, value: toBase64(JSON.stringify(record)) }),
  });
  if (res.status === 412) {
    return false;
  }
  if (!res.ok) {
    throw new Error(`checkpoint CAS for ${sessionID} failed: ${res.status}`);
  }
  return true;
}

// maybeCheckpoint writes a checkpoint every CHECKPOINT_EVERY-th turn. Before
// overwriting, it reads the EXISTING checkpoint's boundary and the tail of
// events since that old boundary -- that tail (not an empty one read after
// the new boundary is already written) is the demonstrable "projection":
// what a real agent would read instead of full history to resume context.
// The KV CAS conflict path is bounded and best-effort: unlike EventLog
// append, a KV CAS 412 body carries no head/version to resync from
// (writeStoreErr's generic mapping, no Head field attached), so a conflict
// here just re-reads the current version and retries up to
// MAX_CHECKPOINT_ATTEMPTS before giving up until the next cycle.
async function maybeCheckpoint(sessionID, turn, session, head) {
  const turnNumber = turn + 1;
  if (turnNumber % CHECKPOINT_EVERY !== 0) {
    return null;
  }

  for (let attempt = 0; attempt < MAX_CHECKPOINT_ATTEMPTS; attempt++) {
    const existing = await getCheckpoint(sessionID);
    const boundary = existing.record ? existing.record.coveredThroughSeq : 0;
    const tail = await eventLogRead(sessionID, boundary, 200);

    const record = {
      v: 1,
      coveredThroughSeq: head,
      kind: 'text-summary',
      payload: {
        summary: session.summary,
        turnsCovered: turnNumber,
        projectedFromSeq: boundary,
        tailEvents: tail.length,
      },
      createdAt: nowISO(),
      turn: turn,
    };

    if (await writeCheckpointCAS(sessionID, record, existing.version)) {
      return { record: record, tailCount: tail.length, boundary: boundary };
    }
    // 412: another writer moved the checkpoint since our GET -- resync (the
    // next loop iteration's getCheckpoint) rather than blindly retry the
    // same expectVersion.
  }

  console.error(`support-desk: session ${sessionID} turn ${turn}: checkpoint CAS conflict, skipping this cycle`);
  return null;
}

async function loadSession(sessionID) {
  if (!stateCreds) {
    return memoryFallback.get(sessionID) || { turns: 0, summary: '' };
  }
  const res = await fetch(`${STATE_URL}/v1/state/${encodeURIComponent(sessionID)}`, {
    headers: stateHeaders(),
  });
  if (res.status === 404) {
    return { turns: 0, summary: '' };
  }
  if (!res.ok) {
    throw new Error(`state GET ${sessionID} failed: ${res.status}`);
  }
  return res.json();
}

async function saveSession(sessionID, session) {
  if (!stateCreds) {
    memoryFallback.set(sessionID, session);
    return;
  }
  const res = await fetch(`${STATE_URL}/v1/state/${encodeURIComponent(sessionID)}`, {
    method: 'PUT',
    headers: stateHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify(session),
  });
  if (!res.ok) {
    throw new Error(`state PUT ${sessionID} failed: ${res.status}`);
  }
}

// summarize folds one turn's message into a short running summary: bounded
// length, no LLM call involved — this fixture is load-generator fodder for
// the density/teleport story, not a real support agent.
function summarize(prevSummary, message) {
  const next = prevSummary ? `${prevSummary} | ${message}` : message;
  return next.length > MAX_SUMMARY_CHARS ? next.slice(next.length - MAX_SUMMARY_CHARS) : next;
}

function extractMessage(body) {
  if (typeof body === 'string' && body.trim() !== '') {
    return body;
  }
  if (body && typeof body === 'object' && typeof body.message === 'string') {
    return body.message;
  }
  return '(empty turn)';
}

// artifactRequested reports whether body carries {"artifact":true} -- a
// string body (extractMessage's plain-message shape) never requests the
// beat, matching spawn.js's identical parsing of its own {"spawn":true}
// flag.
function artifactRequested(body) {
  return !!(body && typeof body === 'object' && body.artifact === true);
}

module.exports = async function (context) {
  const req = context.request;
  const sessionID = req.headers[SESSION_HEADER] || 'unknown';
  const message = extractMessage(req.body);
  const turn = parseTurnHeader(req.headers[TURNS_HEADER]);

  let session;
  try {
    session = await loadSession(sessionID);
  } catch (err) {
    console.error(`support-desk: loading session ${sessionID} failed: ${err}`);
    session = { turns: 0, summary: '' };
  }

  session.turns = (session.turns || 0) + 1;
  session.summary = summarize(session.summary || '', message);

  try {
    await saveSession(sessionID, session);
  } catch (err) {
    console.error(`support-desk: saving session ${sessionID} failed: ${err}`);
  }

  // Real per-turn history + checkpoint, on top of the KV blob above. Only
  // attempted when the state API is reachable (stateCreds set) -- the
  // memoryFallback path skips this entirely, same as it always has for the
  // KV blob's own state-unavailable case.
  let projection = null;
  if (stateCreds) {
    try {
      const head = await appendTurnHistory(sessionID, turn, message);
      projection = await maybeCheckpoint(sessionID, turn, session, head);
    } catch (err) {
      console.error(`support-desk: history/checkpoint for session ${sessionID} turn ${turn} failed: ${err}`);
    }
  }

  let replyText = `Turn ${session.turns} for session ${sessionID}. Running summary: ${session.summary}`;
  if (projection) {
    replyText +=
      ` | checkpoint written through seq ${projection.record.coveredThroughSeq}` +
      ` (projected from seq ${projection.boundary}, ${projection.tailCount} event(s) since boundary)`;
  }

  const reply = {
    session: sessionID,
    turn: session.turns,
    summary: session.summary,
    reply: replyText,
  };

  // artifact* fields are only added when this turn's body asked for the
  // beat -- omitted entirely on every other turn, rather than running it
  // unconditionally on every turn.
  if (artifactRequested(req.body)) {
    const artifact = await artifactBeat(sessionID, turn);
    reply.artifactSha256Put = artifact.artifactSha256Put;
    reply.artifactSha256Got = artifact.artifactSha256Got;
    reply.artifactList = artifact.artifactList;
  }

  return {
    status: 200,
    body: JSON.stringify(reply),
    headers: {
      'Content-Type': 'application/json',
      [YIELD_HEADER]: 'waiting',
    },
  };
};
