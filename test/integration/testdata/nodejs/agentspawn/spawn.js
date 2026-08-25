// Agent-runtime spawn-parentage fixture (RFC-0031 slice 5 / parent-graph
// plan, Task 5). A minimal cousin of
// demo/agent-boardroom/fixtures/architect.js: on a turn whose JSON body
// carries {"spawn":true}, it derives ONE child session id for stepKey
// "expert-1" and dispatches ONE turn to that child on ITSELF (same
// namespace, same function) with X-Fission-Session set to the derived id and
// X-Fission-Agent-Parent set to this session's own ParentRef. Every turn
// (spawn or not) replies 200 with {"session","parent","spawned","spawnStatus",
// "spawnBody","agentTokenSha256","agentTokenNamespace","agentTokenAgent"},
// where "spawned" is the derived child id (or null when this turn did not
// spawn) and spawnStatus/spawnBody are the inner dispatch's own
// last-observed HTTP status and response body (0/"" when no spawn was
// attempted, or on a network error before any response arrived) -- present
// specifically so a caller-side test can name what the inner dispatch
// actually saw instead of just timing out on a registry 404. The
// agentToken* fields are the Task 6 injection-chain proof (see the "Auth"
// paragraph below): they NEVER carry the raw token, only its sha256 hex (or
// the literal "absent") plus the file's claimed namespace/agent, so a
// caller-side test can independently re-derive the expected token and
// compare hashes without a live bearer credential ever touching test
// output/logs.
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
// architect.js's header comment for the same auth/G16 writeup, which applies
// identically here (this fixture calls the same full-privilege dispatch
// route architect.js does).
//
// Auth (G16 gate LIFTED): this pod carries its own per-agent identity, a
// {namespace, agent, token} credential the fetcher writes to the file
// FISSION_AGENT_TOKEN_PATH points at (pkg/fetcher/agenttoken.go) at
// specialize time. This fixture reads it once at module load and sends it on
// the spawn dispatch as Authorization: Bearer <token> plus the
// X-Fission-Agent-Auth-Namespace / X-Fission-Agent-Auth-Name claims headers
// (pkg/agentruntime/identity.go) -- see readAgentCredentials()/authHeaders()
// below for the fallback order and the dev-placeholder edge case.

'use strict';

const fs = require('fs');
const crypto = require('crypto');

const SESSION_HEADER = 'x-fission-session'; // express lower-cases header names
const PARENT_HEADER = 'X-Fission-Agent-Parent';
const STEP_KEY = 'expert-1';

const SELF_NAMESPACE = process.env.SELF_NAMESPACE || 'default';
const SELF_AGENT_NAME = process.env.SELF_AGENT_NAME || '';

// DEFAULT_AGENT_RUNTIME_URL mirrors architect.js's constant of the same
// name: the agentruntime Service's in-cluster DNS name, correct only for
// install-namespace "fission". Prefer FISSION_AGENT_RUNTIME_URL (injected by
// the executor, pkg/executor/util/agentenv.go); the legacy AGENT_RUNTIME_URL
// manual override and this hardcoded default remain as fallbacks.
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
// fetcher at specialize time (pkg/fetcher/agenttoken.go).
const AGENT_TOKEN_PATH = process.env.FISSION_AGENT_TOKEN_PATH || '';

// DEV_PLACEHOLDER_TOKEN is the fetcher's un-derived stand-in
// (pkg/fetcher/agenttoken.go writeAgentTokenFile): written when the fetcher
// itself has no FISSION_INTERNAL_AUTH_SECRET, so it never verifies as a real
// identity token. Preferring it anyway would matter on one non-default
// install shape: authentication.enabled but internalAuth.enabled=false, where
// the file carries the placeholder and file-first-always would 401 where a
// manually-provisioned AGENT_RUNTIME_TOKEN JWT would have worked -- so
// authHeaders() below falls through to the env token in that case instead.
const DEV_PLACEHOLDER_TOKEN = 'dev-unauthenticated';

// readAgentCredentials reads the fetcher-written AgentCredentials JSON
// ({namespace, agent, token}) once at module load -- not per spawn call --
// and caches it in agentCreds below.
function readAgentCredentials(path) {
  if (!path) {
    return null;
  }
  try {
    return JSON.parse(fs.readFileSync(path, 'utf8'));
  } catch (err) {
    console.error(`agentspawn: could not read agent identity token file ${path}: ${err}`);
    return null;
  }
}

const agentCreds = readAgentCredentials(AGENT_TOKEN_PATH);

// agentTokenSha256/agentTokenNamespace/agentTokenAgent are computed once at
// module load (agentCreds is read once, not per turn) and echoed on EVERY
// turn's response so TestAgentRuntimeSpawnParentage
// (agentruntime_test.go, Task 6 of the parent-graph plan) can prove the
// injection chain end-to-end: the fetcher wrote this pod a credential file
// (pkg/fetcher/agenttoken.go) whose token the Go test independently
// re-derives via hmacauth.DeriveAgentIdentityKey and compares by sha256 --
// NEVER by echoing the raw token itself, which would leak a live bearer
// credential into test output/logs. agentTokenSha256 is the literal string
// "absent" when the credentials file is missing or unreadable (see
// readAgentCredentials's catch above); the namespace/agent claims are empty
// strings in that same case.
const agentTokenSha256 =
  agentCreds && agentCreds.token
    ? crypto.createHash('sha256').update(agentCreds.token).digest('hex')
    : 'absent';
const agentTokenNamespace = agentCreds ? agentCreds.namespace || '' : '';
const agentTokenAgent = agentCreds ? agentCreds.agent || '' : '';

// spawnSessionID is the JS twin of pkg/agentruntime/session.go's
// SpawnSessionID: 'c-' + the first 32 lowercase hex characters of
// sha256(parentRef + "/" + stepKey).
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
      // Best-effort body read for diagnostics -- res.text() itself should
      // not throw here (the response already completed with a status), but
      // this is a test fixture's failure-reporting path, not the happy
      // path, so it stays defensive anyway.
      let bodyText = '';
      try {
        bodyText = await res.text();
      } catch (err) {
        bodyText = `<failed to read body: ${err}>`;
      }
      console.error(`agentspawn: spawn ${stepKey} (${childID}) failed: HTTP ${res.status} body=${bodyText}`);
      return { childID: childID, ok: false, status: res.status, body: bodyText };
    }
    return { childID: childID, ok: true, status: res.status, body: '' };
  } catch (err) {
    console.error(`agentspawn: spawn ${stepKey} (${childID}) failed: ${err}`);
    return { childID: childID, ok: false, status: 0, body: '', error: String(err) };
  }
}

module.exports = async function (context) {
  const req = context.request;
  const sessionID = req.headers[SESSION_HEADER] || 'unknown';
  // Canonical ParentRef form (session.go's ParentRef.String()):
  // "<namespace>/<agent>/<id>".
  const parentRef = `${SELF_NAMESPACE}/${SELF_AGENT_NAME}/${sessionID}`;

  // req.body arrives pre-parsed as an object for a JSON content type in the
  // common case, but support-desk.js (demo/agent-boardroom/fixtures/, not
  // this testdata family) defends against a raw-string body too -- mirror
  // that defensiveness here rather than assume the parsed-object case always
  // holds.
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
  // spawnStatus/spawnBody carry that inner dispatch's own last-observed
  // outcome (0/"" when nothing was attempted) so a caller-side test failure
  // can name what actually happened inside the pod, instead of surfacing
  // only as a generic registry-side 404 after its own timeout.
  let spawned = null;
  let spawnStatus = 0;
  let spawnBody = '';
  if (shouldSpawn) {
    const result = await spawnChild(parentRef, STEP_KEY);
    spawned = result.childID;
    spawnStatus = result.status;
    spawnBody = result.error ? `<network error: ${result.error}>` : result.body;
  }

  return {
    status: 200,
    body: JSON.stringify({
      session: sessionID,
      parent: parentRef,
      spawned: spawned,
      spawnStatus: spawnStatus,
      spawnBody: spawnBody,
      agentTokenSha256: agentTokenSha256,
      agentTokenNamespace: agentTokenNamespace,
      agentTokenAgent: agentTokenAgent,
    }),
    headers: { 'Content-Type': 'application/json' },
  };
};
