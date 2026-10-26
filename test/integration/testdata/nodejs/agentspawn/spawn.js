// Agent-runtime spawn-parentage fixture (RFC-0031 slice 5 / parent-graph
// plan, Task 5). A minimal cousin of
// demo/agent-boardroom/fixtures/architect.js: on a turn whose JSON body
// carries {"spawn":true}, it derives ONE child session id for stepKey
// "expert-1" and dispatches ONE turn to that child on ITSELF (same
// namespace, same function) with X-Fission-Session set to the derived id and
// X-Fission-Agent-Parent set to this session's own ParentRef. Every turn
// (spawn or not) replies 200 with {"session","parent","spawned","spawnStatus",
// "spawnBody","agentTokenSha256","agentTokenNamespace","agentTokenAgent",
// "traceparentSeen"},
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
// its own namespace: the test already knows its own namespace and function
// name before creating the function, so it TEMPLATES both into this source
// (the __SELF_NAMESPACE__ / __SELF_AGENT_NAME__ placeholders below) via
// framework.WriteTestDataReplacing. They are deliberately NOT passed as
// function env vars: env/envFrom is rejected by the vfunction webhook on the
// poolmgr executor (RFC-0030 phase 2), and these agent fixtures run on
// poolmgr. process.env still wins when set, so a non-poolmgr / non-templated
// run can override.
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
//
// Session workspace beat (G12, Task 4 of the workspace plan): on a turn
// whose body carries {"artifact":true} this fixture PUTs a generated few-KiB
// text artifact to its OWN session workspace at "notes/summary.txt"
// (pkg/agentruntime/workspace.go), GETs it straight back, and lists the
// session — the SAME identity credential/authHeaders() plumbing above
// authenticates these calls, since workspace.go's own-workspace check
// requires the IDENTICAL claims a spawn dispatch already carries (namespace
// AND agent, not just namespace). Best-effort like spawnChild above: a
// failed PUT/GET/LIST never fails this turn's own 200, it only reports
// "error:<code>" in the corresponding response field(s) — see
// artifactBeat's doc comment.

'use strict';

const fs = require('fs');
const crypto = require('crypto');

const SESSION_HEADER = 'x-fission-session'; // express lower-cases header names
const PARENT_HEADER = 'X-Fission-Agent-Parent';
const STEP_KEY = 'expert-1';

// __SELF_NAMESPACE__ / __SELF_AGENT_NAME__ are replaced with real values by
// framework.WriteTestDataReplacing at function-create time (see the header
// comment): they cannot be function env vars on poolmgr. They are NOT read from
// the agentCreds file this fixture parses below either — that file is written
// only once the pod specializes AFTER `fn update --agent`, so it is absent in
// the create-then-update window, whereas these two must be correct
// deterministically from turn 1 (they build the dispatch URL and ParentRef).
const SELF_NAMESPACE = process.env.SELF_NAMESPACE || '__SELF_NAMESPACE__';
const SELF_AGENT_NAME = process.env.SELF_AGENT_NAME || '__SELF_AGENT_NAME__';

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
    // Log err.code/err.name only: a mid-token corrupt-file parse failure
    // embeds a source snippet in err.message (V8 JSON.parse SyntaxError),
    // and the token is the last field of this JSON -- stringifying err
    // could leak token bytes into pod logs.
    console.error(`agentspawn: could not read agent identity token file ${path}: ${err.code || err.name}`);
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

// ARTIFACT_PATH is the fixed within-session path this beat round-trips,
// matching the demo fixture's own choice (demo/agent-boardroom/fixtures/
// support-desk.js) and the integration test's assertion
// (agentruntime_test.go).
const ARTIFACT_PATH = 'notes/summary.txt';
// ARTIFACT_MIN_BYTES bounds the generated content to "a few KiB" (Task 4's
// brief) -- well under AGENT_MAX_ARTIFACT_BYTES' 32MiB default, so this beat
// never needs to reason about the quota-rejection paths (workspace_test.go's
// TestWorkspacePut_OverCap413_NothingStored etc. already cover those at the
// unit level).
const ARTIFACT_MIN_BYTES = 3072;

// workspaceURL builds the wire URL for one of the four session workspace
// routes (workspace.go's mountWorkspaceRoutes): the three path-scoped verbs
// when path is given, the list route when it is omitted.
function workspaceURL(ns, agent, sessionID, path) {
  const base = `${AGENT_RUNTIME_URL}/workspace/${encodeURIComponent(ns)}/${encodeURIComponent(agent)}/${encodeURIComponent(sessionID)}`;
  return path ? `${base}/${path}` : base;
}

// buildArtifactContent deterministically generates a few-KiB text artifact
// from the session id and turn number -- no randomness, so a re-run against
// the SAME session/turn produces byte-identical content (harmless either
// way, since the PUT/GET sha256 comparison this beat reports only ever
// compares against ITS OWN write within the same turn).
function buildArtifactContent(sessionID, turn) {
  const line = `session ${sessionID} turn ${turn}: session workspace (G12) artifact demo beat -- PUT/GET/LIST round-trip through pkg/agentruntime/workspace.go.\n`;
  let out = '';
  while (out.length < ARTIFACT_MIN_BYTES) {
    out += line;
  }
  return out;
}

// errTag extracts a short, loggable/reportable tag from an artifact-beat
// error: the HTTP status code when putArtifact/getArtifact/listArtifacts
// threw a "status:<code>" Error (see those functions below), otherwise the
// error's own code/name -- never its full message, mirroring
// readAgentCredentials' token-leak guard (irrelevant here, but keeping the
// same shape avoids a second convention for "how do we stringify an error"
// in this file).
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
  await res.json(); // drain/validate the {path,size} body; size is not needed here.
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
// shape this turn's response echoes: artifactSha256Put/artifactSha256Got are
// the literal string "error:<code>" on failure (never thrown -- see this
// file's header comment), and artifactList is always an array; a LIST
// failure reports as a single sentinel item {path:"error:<code>",size:0}
// rather than changing the field's type, so a caller-side JSON decoder never
// has to handle two different shapes for the same field.
async function artifactBeat(ns, agent, sessionID, turn) {
  const content = buildArtifactContent(sessionID, turn);

  let put = 'error:unknown';
  try {
    put = await putArtifact(ns, agent, sessionID, content);
  } catch (err) {
    console.error(`agentspawn: artifact PUT (session ${sessionID}) failed: ${err}`);
    put = `error:${errTag(err)}`;
  }

  let got = 'error:unknown';
  try {
    got = await getArtifact(ns, agent, sessionID);
  } catch (err) {
    console.error(`agentspawn: artifact GET (session ${sessionID}) failed: ${err}`);
    got = `error:${errTag(err)}`;
  }

  let list = [{ path: 'error:unknown', size: 0 }];
  try {
    list = await listArtifacts(ns, agent, sessionID);
  } catch (err) {
    console.error(`agentspawn: artifact LIST (session ${sessionID}) failed: ${err}`);
    list = [{ path: `error:${errTag(err)}`, size: 0 }];
  }

  return { artifactSha256Put: put, artifactSha256Got: got, artifactList: list };
}

module.exports = async function (context) {
  const req = context.request;
  const sessionID = req.headers[SESSION_HEADER] || 'unknown';
  // Canonical ParentRef form (session.go's ParentRef.String()):
  // "<namespace>/<agent>/<id>".
  const parentRef = `${SELF_NAMESPACE}/${SELF_AGENT_NAME}/${sessionID}`;

  // traceparentSeen is Task 3 of the agent-runtime observability plan's
  // end-to-end propagation proof: the raw `traceparent` request header this
  // pod saw (or the literal "absent" when none arrived), echoed on EVERY
  // turn's response. traceparent is NOT a secret (it is a public W3C
  // trace-context header, not a credential), so unlike agentTokenSha256
  // above this is safe to echo verbatim rather than hash -- a caller-side
  // test parses it and compares its trace-id against one it sent, proving
  // the header survived POST /agents/... -> the dispatcher's invoke_agent
  // span -> the otelhttp-instrumented outbound call -> the router-internal
  // hop -> this pod, with no collector anywhere in the loop. Express lowers
  // header names (see SESSION_HEADER above), so the plain lowercase key is
  // enough.
  const traceparentSeen = req.headers['traceparent'] || 'absent';

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
  const shouldArtifact = !!(body && typeof body === 'object' && body.artifact === true);

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

  // artifact* fields are only populated when this turn actually ran the
  // beat (shouldArtifact) -- null on every other turn, rather than running
  // the beat unconditionally on every turn this fixture ever handles. This
  // fixture (unlike support-desk.js) tracks no per-session turn count, so
  // buildArtifactContent's turn argument is a fixed 0 -- it only needs to be
  // SOME string, not an accurate counter.
  let artifact = { artifactSha256Put: null, artifactSha256Got: null, artifactList: null };
  if (shouldArtifact) {
    artifact = await artifactBeat(SELF_NAMESPACE, SELF_AGENT_NAME, sessionID, 0);
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
      traceparentSeen: traceparentSeen,
      artifactSha256Put: artifact.artifactSha256Put,
      artifactSha256Got: artifact.artifactSha256Got,
      artifactList: artifact.artifactList,
    }),
    headers: { 'Content-Type': 'application/json' },
  };
};
