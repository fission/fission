'use strict';

// agenthistory/history.js is TestAgentRuntimeHistory's fixture
// (test/integration/suites/common/agentruntime_test.go): the agent-loop
// fixture's turn-header shape (../agentloop/loop.js) merged with the
// RFC-0027 G13 history/checkpoint wire path
// (demo/agent-boardroom/fixtures/support-desk.js, the committed Task 6
// reference implementation this fixture mirrors almost line-for-line, minus
// the demo's KV session blob and simulated tool-call payloads — this is a
// wire-path test, not a demo, so it writes only the two events the platform
// side actually asserts on).
//
// State contract (RFC-0023, see pkg/fetcher/statetoken.go and
// pkg/statesvc/handler.go): the fetcher writes a StateCredentials JSON file
// {namespace, keyspace, token} to FISSION_STATE_TOKEN_PATH at specialize
// time; FISSION_STATE_URL is the statesvc base URL.
//
//   POST {FISSION_STATE_URL}/v1/eventlog/append   {stream,expectedSeq,events}
//   POST {FISSION_STATE_URL}/v1/eventlog/read     {stream,fromSeq,limit}
//   POST {FISSION_STATE_URL}/v1/eventlog/head     {stream}
//   GET  {FISSION_STATE_URL}/v1/state/agentckpt.{sessionID}
//   POST {FISSION_STATE_URL}/v1/state/agentckpt.{sessionID}/cas
//        {expectVersion,value}
//
// The history stream name ("agentlog/" + sessionID) and the checkpoint key
// prefix ("agentckpt.") must match pkg/agentruntime/history.go's
// HistoryStreamPrefix / CheckpointKeyPrefix exactly — the platform side reads
// through those same names, never hand-derived twice.
//
// Per turn this fixture appends exactly two history events (turn_start,
// yield) keyed by the dispatcher's X-Fission-Agent-Turns header (the
// session's turn count from BEFORE this turn — stable across at-least-once
// redelivery of the SAME turn, which is why it, not a locally-incremented
// counter, is the replay dedup key below). It writes a checkpoint on every
// CHECKPOINT_EVERY-th turn (1-based turn number): with CHECKPOINT_EVERY=2,
// that lands on the SECOND turn dispatched (0-based turn=1) — half the
// demo's every-5th-turn cadence, since two turns are all this test needs.

const fs = require('fs');

const SESSION_HEADER = 'x-fission-session'; // express lower-cases header names
const TURNS_HEADER = 'x-fission-agent-turns';

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
const CHECKPOINT_EVERY = 2;
// MAX_APPEND_ATTEMPTS bounds the EventLog CAS-append retry loop: one
// straight-through attempt plus a few resync-and-retry cycles after a 412.
const MAX_APPEND_ATTEMPTS = 4;

const STATE_URL = process.env.FISSION_STATE_URL || '';
const TOKEN_PATH = process.env.FISSION_STATE_TOKEN_PATH || '';

let stateCreds = null;
if (STATE_URL && TOKEN_PATH) {
  try {
    stateCreds = JSON.parse(fs.readFileSync(TOKEN_PATH, 'utf8'));
  } catch (err) {
    // err.code/err.name only: a raw JSON.parse SyntaxError embeds a source
    // snippet, and the token is the last field in the file.
    console.error(`agenthistory: could not read state token file ${TOKEN_PATH}: ${err.code || err.name}`);
  }
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

// parseTurnHeader reads TURNS_HEADER; an absent or unparsable header falls
// back to 0 rather than throwing.
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

// buildTurnEvents returns the two synthetic events for one turn: turn_start
// (step 0) and yield (step 1). Every payload carries the
// {v,turn,step,kind,at} envelope the wire contract specifies.
function buildTurnEvents(turn) {
  const specs = [
    { step: 0, kind: 'turn_start', detail: {} },
    { step: 1, kind: 'yield', detail: {} },
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
// returns {conflict:true, head} (head taken from the error envelope) instead
// of throwing, so the caller can resync and retry.
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
// double-append a step.
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
      // not a v1 {turn,step} payload this fixture understands.
    }
  }
  return pending.filter(function (e) {
    return !present.has(e.payload.step);
  });
}

// appendTurnHistory CAS-appends this turn's history events, replay-safe
// against both a genuine 412 (concurrent writer) and a clean at-least-once
// redelivery that never conflicts at all. Returns the stream's head after
// this turn's events are present (whether this call wrote them or a prior
// delivery did).
async function appendTurnHistory(sessionID, turn) {
  let head = await eventLogHead(sessionID);
  let pending = buildTurnEvents(turn);

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
      `agenthistory: session ${sessionID} turn ${turn}: gave up appending ${pending.length} history event(s) after ${MAX_APPEND_ATTEMPTS} attempts`,
    );
  }

  return head;
}

// getCheckpoint reads the current checkpoint record's KV version, so a
// subsequent CAS write knows the right expectVersion. A 404 (no checkpoint
// yet) is not an error: version 0 means create-only.
async function getCheckpoint(sessionID) {
  const res = await fetch(`${STATE_URL}/v1/state/${encodeURIComponent(checkpointKey(sessionID))}`, {
    headers: stateHeaders(),
  });
  if (res.status === 404) {
    return { version: 0 };
  }
  if (!res.ok) {
    throw new Error(`checkpoint GET for ${sessionID} failed: ${res.status}`);
  }
  const version = parseInt(res.headers.get(STATE_VERSION_HEADER) || '0', 10);
  return { version: version };
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

// maybeCheckpoint writes a checkpoint on every CHECKPOINT_EVERY-th turn
// (1-based turn number). record.turn stays the 0-based turn value, matching
// pkg/agentruntime/history.go's CheckpointRecord.Turn field and
// support-desk.js's own record shape.
async function maybeCheckpoint(sessionID, turn, head) {
  const turnNumber = turn + 1;
  if (turnNumber % CHECKPOINT_EVERY !== 0) {
    return;
  }

  const existing = await getCheckpoint(sessionID);
  const record = {
    v: 1,
    coveredThroughSeq: head,
    kind: 'text-summary',
    payload: { note: `checkpoint at turn ${turn}` },
    createdAt: nowISO(),
    turn: turn,
  };

  if (!(await writeCheckpointCAS(sessionID, record, existing.version))) {
    console.error(`agenthistory: session ${sessionID} turn ${turn}: checkpoint CAS conflict, skipping`);
  }
}

module.exports = async function (context) {
  const req = context.request;
  const sessionID = req.headers[SESSION_HEADER] || 'unknown';
  const turn = parseTurnHeader(req.headers[TURNS_HEADER]);

  let head = null;
  if (stateCreds) {
    try {
      head = await appendTurnHistory(sessionID, turn);
      await maybeCheckpoint(sessionID, turn, head);
    } catch (err) {
      console.error(`agenthistory: history/checkpoint for session ${sessionID} turn ${turn} failed: ${err}`);
    }
  } else {
    console.error(`agenthistory: no state credentials (FISSION_STATE_URL/FISSION_STATE_TOKEN_PATH unset); skipping history/checkpoint for session ${sessionID}`);
  }

  return {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session: sessionID, turn: turn, head: head }),
  };
};
