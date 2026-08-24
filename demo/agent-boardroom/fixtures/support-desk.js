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
// time; FISSION_STATE_URL is the statesvc base URL. This fixture keeps it
// minimal, per the task brief: one JSON key per session (turn count + a
// short running summary), fetched with GET and written back with PUT.
//
//   GET  {FISSION_STATE_URL}/v1/state/{sessionID}
//   PUT  {FISSION_STATE_URL}/v1/state/{sessionID}
//   Authorization: Bearer <token>
//   X-Fission-State-Namespace: <namespace>
//   X-Fission-State-Keyspace: <keyspace>
//
// When FISSION_STATE_URL/FISSION_STATE_TOKEN_PATH are unset (functionState
// disabled, or a dev cluster with no statestore) the fixture falls back to
// an in-process Map so it still answers turns — that memory does not survive
// a pod recycle, unlike the real state-backed path.

const fs = require('fs');

const SESSION_HEADER = 'x-fission-session'; // express lower-cases header names
const YIELD_HEADER = 'X-Fission-Agent-Yield';
const MAX_SUMMARY_CHARS = 240;

const STATE_URL = process.env.FISSION_STATE_URL || '';
const TOKEN_PATH = process.env.FISSION_STATE_TOKEN_PATH || '';

let stateCreds = null;
if (STATE_URL && TOKEN_PATH) {
  try {
    stateCreds = JSON.parse(fs.readFileSync(TOKEN_PATH, 'utf8'));
  } catch (err) {
    console.error(`support-desk: could not read state token file ${TOKEN_PATH}: ${err}`);
  }
}

// memoryFallback is only ever used when the state API is unreachable; it is
// pod-local and lost on every recycle, so it must never be mistaken for the
// real RFC-0023 keyspace.
const memoryFallback = new Map();

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

module.exports = async function (context) {
  const req = context.request;
  const sessionID = req.headers[SESSION_HEADER] || 'unknown';
  const message = extractMessage(req.body);

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

  const reply = {
    session: sessionID,
    turn: session.turns,
    summary: session.summary,
    reply: `Turn ${session.turns} for session ${sessionID}. Running summary: ${session.summary}`,
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
