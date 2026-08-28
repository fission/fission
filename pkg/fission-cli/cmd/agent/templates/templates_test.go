// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/fetcher"

	// Test-only imports of platform packages the shipped CLI binary never
	// links against (pkg/fission-cli/cmd/agent/sessions.go:94-95 states the
	// no-import-of-pkg/agentruntime convention that binds the binary). A
	// _test.go file is excluded from the non-test build, so importing
	// agentruntime here to read its exported header constants cannot pull
	// the runtime into `fission`'s binary -- it only pins these templates
	// against dispatcher.go's actual constants instead of re-typed string
	// literals, so a header rename there breaks this test, not a silent
	// scaffold that talks past the real dispatcher.
	"github.com/fission/fission/pkg/agentruntime"
	"github.com/fission/fission/pkg/statesvc/stateapi"
)

// commentPrefix returns the line-comment marker for a rendered language, so
// tests can tell an explanatory mention of a yield value from a live one.
func commentPrefix(lang string) string {
	if lang == "python" {
		return "#"
	}
	return "//"
}

// render is a small helper: render kind/lang's template and require success.
func render(t *testing.T, kind, lang string) ([]byte, string) {
	t.Helper()
	out, ext, err := Render(kind, lang, "widget")
	require.NoError(t, err)
	require.NotEmpty(t, out)
	return out, ext
}

// renderAgent is the pre-PR-1 default-kind shorthand every test below that
// predates the --template flag still uses.
func renderAgent(t *testing.T, lang string) ([]byte, string) {
	t.Helper()
	return render(t, "agent", lang)
}

func TestRender_UnsupportedLanguage(t *testing.T) {
	t.Parallel()
	_, _, err := Render("agent", "ruby", "widget")
	require.Error(t, err)
}

func TestRender_UnsupportedTemplateKind(t *testing.T) {
	t.Parallel()
	_, _, err := Render("wizard", "node", "widget")
	require.Error(t, err)
}

// TestRender_RejectsUnsafeNames is the regression test for the
// code-injection finding from Task 1 review: {{.Name}} is interpolated
// inside JS backtick template literals and Python %-format single-quoted
// strings in both templates, so an unvalidated name containing a backtick
// or a single quote can break out of the string literal and land arbitrary
// text in LIVE, non-comment code (confirmed exploitable against the
// pre-fix build: a name of the shape
// "x`); require('child_process').execSync('touch /tmp/PWNED'); console.log(`"
// rendered with the injected call sitting outside any comment). Render must
// reject every name below with an error and MUST NOT emit any of the
// injected payload text into the rendered bytes -- Task 2 also validates
// the name, but it writes the scaffolded file to disk before any k8s-side
// validation runs, so Render has to be safe standing alone.
func TestRender_RejectsUnsafeNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
	}{
		{name: "backtick breakout (JS)", payload: "x`); require('child_process').execSync('touch /tmp/PWNED'); console.log(`"},
		{name: "single-quote breakout (Python)", payload: "x'); __import__('os').system('touch /tmp/PWNED'); print('"},
		{name: "go-template metachars", payload: "widget{{.Name}}"},
		{name: "embedded newline", payload: "widget\ninjected := 1"},
		{name: "uppercase (not DNS-1123, but worth covering)", payload: "Widget"},
		{name: "empty", payload: ""},
	}
	// Both kinds share ValidateKubeName as the actual gate (it runs before
	// either template is even parsed), but the loop covers "agent" AND
	// "interpreter" anyway: agent's own backtick/single-quote cases are the
	// documented regression, and running the full matrix against
	// interpreter too catches any future template that grows its own
	// live-code interpolation of {{.Name}} without a matching test.
	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			for _, tc := range cases {
				t.Run(kind+"/"+lang+"/"+tc.name, func(t *testing.T) {
					t.Parallel()
					out, _, err := Render(kind, lang, tc.payload)
					require.Error(t, err, "Render must reject unsafe name %q", tc.payload)
					assert.Empty(t, out, "Render must not return partial output on a rejected name")
				})
			}
		}
	}
}

func TestRender_Extensions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		_, ext := render(t, kind, "node")
		assert.Equal(t, "js", ext)
		_, ext = render(t, kind, "python")
		assert.Equal(t, "py", ext)
	}
}

func TestRender_NoTemplateArtifacts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			out, _ := render(t, kind, lang)
			text := string(out)
			assert.NotContains(t, text, "{{", "%s/%s template left an unrendered placeholder", kind, lang)
			// render(t, kind, lang) passes name "widget" -- confirm
			// substitution actually ran, not merely that the placeholder
			// syntax is gone.
			assert.Contains(t, text, "widget", "%s/%s: Name substitution did not run", kind, lang)
		}
	}
}

// TestRender_HeaderNames pins the templates against the dispatcher's
// EXPORTED header constants (pkg/agentruntime/dispatcher.go) rather than
// re-typed literals: a header rename there fails this test instead of
// silently drifting the scaffold out of sync with the real wire contract.
// The Node template reads request headers via Node's lower-cased header
// map, so it is checked case-insensitively; the yield header is one this
// template SETS on its own response, so it keeps the constant's natural
// case and is checked exactly.
func TestRender_HeaderNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		lang        string
		foldToLower bool
	}{
		{lang: "node", foldToLower: true},
		{lang: "python", foldToLower: false},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			t.Parallel()
			out, _ := renderAgent(t, tc.lang)
			text := string(out)

			session, turns := agentruntime.HeaderSession, agentruntime.HeaderTurns
			if tc.foldToLower {
				text = strings.ToLower(text)
				session = strings.ToLower(session)
				turns = strings.ToLower(turns)
			}
			assert.Contains(t, text, session, "%s: session header const not found", tc.lang)
			assert.Contains(t, text, turns, "%s: turns header const not found", tc.lang)

			// HeaderYield is set verbatim (not folded) in both templates.
			assert.Contains(t, string(out), agentruntime.HeaderYield, "%s: yield header const not found", tc.lang)
		})
	}
}

// TestRender_StateHeaderNames pins the templates' state-scope claim headers
// against pkg/statesvc/stateapi's exported consts (stateapi.go:25-26) rather
// than the re-typed "X-Fission-State-Namespace" / "X-Fission-State-Keyspace"
// literals both templates used to carry unpinned -- same drift class as
// TestRender_HeaderNames, just against statesvc's wire contract instead of
// the dispatcher's.
func TestRender_StateHeaderNames(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"node", "python"} {
		out, _ := renderAgent(t, lang)
		text := string(out)
		assert.Contains(t, text, stateapi.HeaderNamespace, "%s: missing state namespace header", lang)
		assert.Contains(t, text, stateapi.HeaderKeyspace, "%s: missing state keyspace header", lang)
	}
}

// TestRender_StateEnvVarNames pins the templates against
// pkg/executor/util's StateAPIEnvVars, whose injected names
// (FISSION_STATE_URL / FISSION_STATE_TOKEN_PATH) are string literals with
// no exported consts (stateenv.go:33-34) -- this is the zero-prod-change
// drift tripwire the plan review called for: the names below are read from
// the SAME function the executor calls in production, under t.Setenv, so a
// literal rename there fails this test without touching any prod code path.
func TestRender_StateEnvVarNames(t *testing.T) {
	t.Setenv("STATESVC_URL", "http://statesvc.fission:8892")

	envVars := util.StateAPIEnvVars("/userfunc")
	require.NotEmpty(t, envVars, "STATESVC_URL was set; StateAPIEnvVars must not return nil")

	for _, lang := range []string{"node", "python"} {
		out, _ := renderAgent(t, lang)
		text := string(out)
		for _, ev := range envVars {
			assert.Contains(t, text, ev.Name, "%s: missing state env var %s", lang, ev.Name)
		}
	}
}

// TestRender_StateCredentialsFieldNames pins the templates' state-token
// field access against fetcher.StateCredentials's actual JSON tags
// (statetoken.go:29-33) via reflection, rather than hand-typed "namespace"
// / "keyspace" / "token" strings that could silently drift from the
// fetcher's real wire struct.
func TestRender_StateCredentialsFieldNames(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(fetcher.StateCredentials{})
	require.Positive(t, typ.NumField())

	var fieldNames []string
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		require.NotEmpty(t, tag, "field %s has no json tag", typ.Field(i).Name)
		name, _, _ := strings.Cut(tag, ",")
		require.NotEmpty(t, name)
		fieldNames = append(fieldNames, name)
	}

	for _, lang := range []string{"node", "python"} {
		out, _ := renderAgent(t, lang)
		text := string(out)
		for _, name := range fieldNames {
			assert.Contains(t, text, name, "%s: missing StateCredentials field %q", lang, name)
		}
	}
}

// TestRender_YieldDefaultsToWaiting asserts the ACTIVE code path yields
// "waiting" and that YieldContinue's value ("continue") appears only inside
// comments -- a scaffold that defaulted to "continue" would self-re-invoke
// forever on a fresh install (constraints.md's "Yield story" ruling).
//
// The check below looks for the QUOTED form ('continue', single-quoted --
// how both languages spell the yield VALUE everywhere it appears in these
// templates, live or commented) rather than the bare word: PR-2's
// hydrate/sync helper legitimately uses `continue` as Python's and JS's own
// loop-control keyword throughout its walk/list loops, in ACTIVE code, and a
// bare-word search would misidentify every one of those as this yield value
// leaking into the active path.
func TestRender_YieldDefaultsToWaiting(t *testing.T) {
	t.Parallel()

	quotedContinue := "'" + agentruntime.YieldContinue + "'"
	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			t.Run(kind+"/"+lang, func(t *testing.T) {
				t.Parallel()
				out, _ := render(t, kind, lang)
				text := string(out)
				prefix := commentPrefix(lang)

				var activeWaiting bool
				for _, line := range strings.Split(text, "\n") {
					trimmed := strings.TrimSpace(line)
					isCode := trimmed != "" && !strings.HasPrefix(trimmed, prefix)

					if strings.Contains(line, quotedContinue) {
						// The quoted yield VALUE must never appear in the
						// ACTIVE code path (a scaffold that yielded it by
						// default would self-re-invoke forever on a fresh
						// install).
						assert.False(t, isCode,
							"%s/%s: %q value found outside a comment: %q", kind, lang, agentruntime.YieldContinue, line)
					}
					if isCode && strings.Contains(line, agentruntime.YieldWaiting) {
						activeWaiting = true
					}
				}
				assert.True(t, activeWaiting,
					"%s/%s: no active (non-comment) line yields %q", kind, lang, agentruntime.YieldWaiting)
			})
		}
	}
}

// TestRender_NodeSyntax runs `node --check` against every rendered Node
// template (both --template kinds). Skipped (with a note) when node is not
// on PATH.
func TestRender_NodeSyntax(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH; skipping node --check")
	}

	for _, kind := range []string{"agent", "interpreter"} {
		out, _ := render(t, kind, "node")
		path := t.TempDir() + "/" + kind + ".js"
		require.NoError(t, os.WriteFile(path, out, 0o600))

		cmd := exec.Command(nodeBin, "--check", path)
		output, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "%s: node --check failed: %s", kind, output)
	}
}

// TestRender_PythonSyntax runs `python3 -m py_compile` against every
// rendered Python template (both --template kinds). Skipped (with a note)
// when python3 is not on PATH.
func TestRender_PythonSyntax(t *testing.T) {
	pyBin, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found on PATH; skipping py_compile")
	}

	for _, kind := range []string{"agent", "interpreter"} {
		out, _ := render(t, kind, "python")
		path := t.TempDir() + "/" + kind + ".py"
		require.NoError(t, os.WriteFile(path, out, 0o600))

		cmd := exec.Command(pyBin, "-m", "py_compile", path)
		output, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "%s: python3 -m py_compile failed: %s", kind, output)
	}
}

// --- interpreter template tripwires (PR-1, abstraction A / Q35) -----------

// TestRender_Interpreter_HeaderNames pins the interpreter template against
// the SAME dispatcher constants TestRender_HeaderNames pins the agent
// template against -- it reads the session header (to name its scratch
// dir) and always sets the yield header, but never touches the turns
// header (it carries no per-turn counter).
func TestRender_Interpreter_HeaderNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		lang        string
		foldToLower bool
	}{
		{lang: "node", foldToLower: true},
		{lang: "python", foldToLower: false},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			t.Parallel()
			out, _ := render(t, "interpreter", tc.lang)
			text := string(out)

			session := agentruntime.HeaderSession
			if tc.foldToLower {
				text = strings.ToLower(text)
				session = strings.ToLower(session)
			}
			assert.Contains(t, text, session, "%s: session header const not found", tc.lang)
			assert.Contains(t, string(out), agentruntime.HeaderYield, "%s: yield header const not found", tc.lang)
		})
	}
}

// TestRender_Interpreter_ExecContractKeys pins every JSON key of the exec
// verb's wire contract (both the request
// keys, code/command/timeoutSeconds, and the response keys,
// stdout/stderr/exitCode/durationMs/truncated) -- this contract has no
// shared Go struct on either side ("zero new platform Go" is the whole
// point of a template-only exec verb), so the rendered template text is
// the only place these key literals live; this test is the drift guard.
func TestRender_Interpreter_ExecContractKeys(t *testing.T) {
	t.Parallel()

	// Anchored on the wire-touching forms (request-key reads and
	// response-literal keys), not bare words: "code"/"stdout" etc. also
	// occur in comment prose, so a bare Contains would keep passing after
	// the actual contract site was renamed.
	wireForms := map[string][]string{
		"node": {
			`body.code`, `body.command`, `body.timeoutSeconds`,
			`stdout: `, `stderr: `, `exitCode: `, `durationMs: `, `truncated: `,
		},
		"python": {
			`payload.get('code')`, `payload.get('command')`, `payload.get('timeoutSeconds')`,
			`'stdout':`, `'stderr':`, `'exitCode':`, `'durationMs':`, `'truncated':`,
		},
	}
	for lang, forms := range wireForms {
		out, _ := render(t, "interpreter", lang)
		text := string(out)
		for _, form := range forms {
			assert.Contains(t, text, form, "%s: missing exec-contract wire form %q", lang, form)
		}
	}
}

// TestRender_Interpreter_TimeoutCeiling pins the request-carried
// timeoutSeconds default (60) and hard ceiling (300) the exec
// contract specifies.
func TestRender_Interpreter_TimeoutCeiling(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"node", "python"} {
		out, _ := render(t, "interpreter", lang)
		text := string(out)
		assert.Contains(t, text, "DEFAULT_TIMEOUT_SECONDS = 60", "%s: default timeout constant not found or changed", lang)
		assert.Contains(t, text, "MAX_TIMEOUT_SECONDS = 300", "%s: timeout ceiling constant not found or changed", lang)
	}
}

// TestRender_Interpreter_TimeoutRejectsNonFinite pins each language's own
// guard against a non-finite `timeoutSeconds` (NaN, +/-Inf): a bare `<= 0`
// comparison never catches NaN (float('nan') <= 0 is False in Python,
// Number.isFinite(NaN) still gates it in JS only because that check exists),
// so a request carrying {"timeoutSeconds": "nan"} would otherwise reach the
// subprocess timeout as-is and crash the turn with an uncaught error instead
// of the {stdout,stderr,exitCode,durationMs,truncated} contract. The Python
// template must call math.isfinite; the JS template must call
// Number.isFinite -- this is the drift guard for both.
func TestRender_Interpreter_TimeoutRejectsNonFinite(t *testing.T) {
	t.Parallel()

	pyOut, _ := render(t, "interpreter", "python")
	assert.Contains(t, string(pyOut), "math.isfinite(seconds)", "python: _clamp_timeout must reject non-finite values via math.isfinite")

	jsOut, _ := render(t, "interpreter", "node")
	assert.Contains(t, string(jsOut), "Number.isFinite(n)", "node: clampTimeout must reject non-finite values via Number.isFinite")
}

// TestRender_Interpreter_OutputCapMatchesStateDefault pins the stdout/stderr
// cap to fv1.DefaultStateMaxValueBytes (pkg/apis/core/v1/const.go) rather
// than a re-typed 262144 literal -- the two must
// stay aligned ("stdout/stderr each capped at 256 KiB (aligns with state
// MaxValueBytes default)"). Both templates spell the cap as the expression
// `256 * 1024` rather than the raw byte count, so this both checks that
// expression is present and that it still equals the platform constant.
func TestRender_Interpreter_OutputCapMatchesStateDefault(t *testing.T) {
	t.Parallel()
	require.EqualValues(t, 256*1024, fv1.DefaultStateMaxValueBytes,
		"platform's DefaultStateMaxValueBytes moved off 256KiB; update the interpreter templates' MAX_OUTPUT_BYTES to match")
	for _, lang := range []string{"node", "python"} {
		out, _ := render(t, "interpreter", lang)
		assert.Contains(t, string(out), "256 * 1024", "%s: output-cap expression not found", lang)
	}
}

// allowedEnvVarsListRe extracts the ALLOWED_ENV_VARS literal (a single-line
// ['A', 'B', ...] / ["A", "B", ...] list in both templates) so
// TestRender_Interpreter_EnvAllowlistExcludesFissionVars can inspect its
// entries without re-parsing JS or Python.
var allowedEnvVarsListRe = regexp.MustCompile(`ALLOWED_ENV_VARS = \[([^\]]*)\]`)

// TestRender_Interpreter_EnvAllowlistExcludesFissionVars is the static
// (non-cluster) half of the env-scrub requirement ("Subprocess env is
// allow-listed ... never pass-through -- FISSION_* vars ... are scrubbed"):
// it asserts the allow-list itself never names a FISSION_-prefixed
// variable, so the subprocess's environment cannot contain one BY
// CONSTRUCTION regardless of what this pod's own environment carries. The
// dynamic half (actually exec `env` against a live pod and assert no
// FISSION_ keys come back) is
// test/integration/suites/common/agent_exec_test.go's env-scrub case.
func TestRender_Interpreter_EnvAllowlistExcludesFissionVars(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"node", "python"} {
		out, _ := render(t, "interpreter", lang)
		text := string(out)

		match := allowedEnvVarsListRe.FindStringSubmatch(text)
		require.Lenf(t, match, 2, "%s: ALLOWED_ENV_VARS list literal not found", lang)

		var names []string
		for _, entry := range strings.Split(match[1], ",") {
			name := strings.Trim(strings.TrimSpace(entry), `'"`)
			if name != "" {
				names = append(names, name)
			}
		}
		require.NotEmpty(t, names, "%s: ALLOWED_ENV_VARS list is empty", lang)
		assert.Contains(t, names, "PATH", "%s: allow-list should carry PATH", lang)
		for _, name := range names {
			assert.False(t, strings.HasPrefix(name, "FISSION_"),
				"%s: ALLOWED_ENV_VARS must never allow-list a FISSION_ var (found %q)", lang, name)
		}
	}
}

// TestRender_Interpreter_TrustStatementMentionsGvisor pins the interpreter
// template's own trust-boundary comment against the
// requirement that the scaffold recommend `--runtime-class gvisor` for
// stronger isolation before running untrusted code.
func TestRender_Interpreter_TrustStatementMentionsGvisor(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"node", "python"} {
		out, _ := render(t, "interpreter", lang)
		assert.Contains(t, string(out), "--runtime-class gvisor", "%s: trust-boundary comment missing the gvisor recommendation", lang)
	}
}

// TestRender_Interpreter_ScratchNameGuardsTraversal pins the review fix for
// the dispatcher's sessionIDPattern (^[A-Za-z0-9._-]{1,128}$,
// pkg/agentruntime/dispatcher.go) legitimately admitting the exact strings
// "." and ".." as a caller-supplied session id: since both templates turn
// the session id into a scratch-dir path SEGMENT
// ($TMPDIR/exec/<segment>), an unguarded "." or ".." resolves to
// $TMPDIR/exec or $TMPDIR itself -- both of which get recursively removed
// at the start and end of every turn. This also has to hold on the direct
// (no-dispatcher) invocation path, where nothing upstream enforces
// sessionIDPattern's charset at all -- so both templates must re-validate
// the FULL charset here, not just reject the two degenerate values, and
// fall back to a fresh scratch name on anything that fails either check.
func TestRender_Interpreter_ScratchNameGuardsTraversal(t *testing.T) {
	t.Parallel()

	pyOut, _ := render(t, "interpreter", "python")
	assert.Contains(t, string(pyOut), "session_id in ('.', '..')",
		"python: scratch-dir name must guard against the exact session ids \".\" and \"..\"")
	assert.Contains(t, string(pyOut), "_SESSION_ID_CHARSET_RE.fullmatch(session_id)",
		"python: scratch-dir name must also be re-validated against the full session-id charset (the dispatcher's sessionIDPattern is not enforced on the direct-invocation path)")

	jsOut, _ := render(t, "interpreter", "node")
	assert.Contains(t, string(jsOut), "sessionID === '.' || sessionID === '..'",
		"node: scratch-dir name must guard against the exact session ids \".\" and \"..\"")
	assert.Contains(t, string(jsOut), "SESSION_ID_CHARSET_RE.test(sessionID)",
		"node: scratch-dir name must also be re-validated against the full session-id charset (the dispatcher's sessionIDPattern is not enforced on the direct-invocation path)")
}

// TestRender_Interpreter_PythonRejectsNonObjectBody pins the review fix for
// a syntactically valid but non-object JSON body ([1,2], "x", 42): parsed
// via request.get_json(silent=True) it comes back truthy, so without an
// isinstance guard payload.get(...) raises an uncaught AttributeError (a
// 500) instead of the exactly-one-of contract's 400. The node template
// already survives this via parseBody/typeof checks, so only python needs
// the pin.
func TestRender_Interpreter_PythonRejectsNonObjectBody(t *testing.T) {
	t.Parallel()
	out, _ := render(t, "interpreter", "python")
	assert.Contains(t, string(out), "isinstance(payload, dict)",
		"python: a non-object JSON body must be coerced to {} before payload.get(...) is called")
}

// TestRender_Interpreter_PythonHandlesSpawnFailure pins the review fix
// making subprocess.Popen failure (e.g. a missing /bin/sh or python3
// binary) symmetric with the node template's spawn() 'error' handler: both
// must map the failure into the {stdout,stderr,exitCode,durationMs,
// truncated} reply instead of letting it raise past the turn as an
// uncaught 500.
func TestRender_Interpreter_PythonHandlesSpawnFailure(t *testing.T) {
	t.Parallel()
	out, _ := render(t, "interpreter", "python")
	assert.Contains(t, string(out), "except OSError as err:",
		"python: _run_exec must catch a failed Popen and answer the exec contract, not raise")
	assert.Contains(t, string(out), "failed to start",
		"python: a failed Popen should report the same 'failed to start' note the node template uses")
}

// --- workspace hydration/sync tripwires (PR-2, abstraction C) --------------
//
// Unlike the PR-1 exec-contract tripwires above (interpreter-only, since the
// exec verb is interpreter-only), every test below loops over BOTH kinds:
// interpreter.{js,py}.tmpl wire the hydrate/sync helper into the exec turn
// automatically, and agent.{js,py}.tmpl carry the SAME helper block as an
// opt-in call (doc 14's "workspaceSync in interpreter.*.tmpl + exported into
// agent.*.tmpl" placement) -- a drift in either copy is a real regression.

// workspaceHelperBlock extracts the text between this template's
// BEGIN/END marker comments (see interpreter.{js,py}.tmpl and
// agent.{js,py}.tmpl) so TestRender_Workspace_HelperBlockIdenticalAcrossKinds
// can diff the two kinds' copies directly, and other tests below can scope
// their assertions to the helper block alone rather than the whole file.
func workspaceHelperBlock(t *testing.T, text, lang string) string {
	t.Helper()
	prefix := "#"
	if lang == "node" {
		prefix = "//"
	}
	begin := prefix + " --- Workspace hydration/sync (PR-2, abstraction C) "
	end := prefix + " --- End workspace hydration/sync helper "
	start := strings.Index(text, begin)
	require.GreaterOrEqualf(t, start, 0, "%s: BEGIN marker for the workspace helper block not found", lang)
	stop := strings.Index(text, end)
	require.GreaterOrEqualf(t, stop, start, "%s: END marker for the workspace helper block not found after BEGIN", lang)
	return text[start:stop]
}

// TestRender_Workspace_HelperBlockIdenticalAcrossKinds pins doc 14's PR-2
// "Placement" requirement directly: agent.*.tmpl's opt-in copy of the
// hydrate/sync helper must be BYTE-IDENTICAL to interpreter.*.tmpl's wired-in
// copy, per language -- the strongest guard against the two silently
// drifting apart (a fix applied to one kind's copy and forgotten in the
// other).
func TestRender_Workspace_HelperBlockIdenticalAcrossKinds(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"node", "python"} {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			interpOut, _ := render(t, "interpreter", lang)
			agentOut, _ := render(t, "agent", lang)
			interpBlock := workspaceHelperBlock(t, string(interpOut), lang)
			agentBlock := workspaceHelperBlock(t, string(agentOut), lang)
			assert.Equal(t, interpBlock, agentBlock,
				"%s: the workspace hydrate/sync helper block must be byte-identical between interpreter.*.tmpl and agent.*.tmpl", lang)
		})
	}
}

// TestRender_Workspace_IdentityHeaderNames pins the hydrate/sync helper's
// outbound auth headers against pkg/agentruntime/identity.go's EXPORTED
// HeaderIdentityNamespace/HeaderIdentityName -- these are headers the
// template SETS on its own outgoing hydrate/sync requests (like the state
// helper's X-Fission-State-* headers, TestRender_StateHeaderNames), so they
// are checked exact-case, no folding, in every kind x lang combination.
func TestRender_Workspace_IdentityHeaderNames(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			t.Run(kind+"/"+lang, func(t *testing.T) {
				t.Parallel()
				out, _ := render(t, kind, lang)
				text := string(out)
				assert.Contains(t, text, agentruntime.HeaderIdentityNamespace,
					"%s/%s: missing identity namespace claim header", kind, lang)
				assert.Contains(t, text, agentruntime.HeaderIdentityName,
					"%s/%s: missing identity name claim header", kind, lang)
			})
		}
	}
}

// TestRender_Workspace_EnvVarNames pins the hydrate/sync helper against
// pkg/executor/util.AgentAPIEnvVars's actual injected names
// (FISSION_AGENT_RUNTIME_URL / FISSION_AGENT_TOKEN_PATH) rather than re-typed
// literals -- same drift-guard shape as TestRender_StateEnvVarNames, against
// the agent identity env vars instead of the state ones.
func TestRender_Workspace_EnvVarNames(t *testing.T) {
	t.Setenv("AGENT_RUNTIME_URL", "http://agentruntime.fission:8888")

	envVars := util.AgentAPIEnvVars("/userfunc")
	require.NotEmpty(t, envVars, "AGENT_RUNTIME_URL was set; AgentAPIEnvVars must not return nil")

	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			out, _ := render(t, kind, lang)
			text := string(out)
			for _, ev := range envVars {
				assert.Containsf(t, text, ev.Name, "%s/%s: missing agent identity env var %s", kind, lang, ev.Name)
			}
		}
	}
}

// TestRender_Workspace_CredentialsFieldNames pins the hydrate/sync helper's
// token-file field access against fetcher.AgentCredentials's actual JSON
// tags (agenttoken.go) via reflection, rather than hand-typed "namespace" /
// "agent" / "token" strings that could silently drift from the fetcher's
// real wire struct -- mirrors TestRender_StateCredentialsFieldNames.
func TestRender_Workspace_CredentialsFieldNames(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(fetcher.AgentCredentials{})
	require.Positive(t, typ.NumField())

	var fieldNames []string
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		require.NotEmpty(t, tag, "field %s has no json tag", typ.Field(i).Name)
		name, _, _ := strings.Cut(tag, ",")
		require.NotEmpty(t, name)
		fieldNames = append(fieldNames, name)
	}

	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			out, _ := render(t, kind, lang)
			text := string(out)
			for _, name := range fieldNames {
				assert.Containsf(t, text, name, "%s/%s: missing AgentCredentials field %q", kind, lang, name)
			}
		}
	}
}

// TestRender_Workspace_ListURLNoTrailingSlash pins the hydrate/sync helper's URL
// construction against the shipped route shape
// (pkg/agentruntime/workspace.go's mountWorkspaceRoutes):
// "/workspace/{namespace}/{name}/{session}" for LIST (no trailing slash --
// the shipped route 400s a trailing-slash request by design) and
// "/workspace/{namespace}/{name}/{session}/{path...}" for GET/PUT. This
// contract has no shared Go struct on the template side ("the SDK for
// scaffolded agents is the template" -- doc 14's PR-2 Placement note), so
// the rendered text is the only place these literals live.
func TestRender_Workspace_ListURLNoTrailingSlash(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			t.Run(kind+"/"+lang, func(t *testing.T) {
				t.Parallel()
				out, _ := render(t, kind, lang)
				text := string(out)
				assert.Contains(t, text, "/workspace/", "%s/%s: missing /workspace/ route segment", kind, lang)
				// The LIST url-builder function must not itself append a
				// trailing slash or a path segment -- it is reused, with
				// "/" + rel appended by the CALLER, for GET/PUT.
				if lang == "python" {
					assert.Contains(t, text, "'%s/workspace/%s/%s/%s' % (\n        WORKSPACE_URL, _workspace_creds['namespace'], _workspace_creds['agent'], session_id)",
						"python: LIST url-builder must build exactly /workspace/{ns}/{agent}/{session} with no trailing slash")
				} else {
					assert.Contains(t, text, "`${WORKSPACE_URL}/workspace/${workspaceCreds.namespace}/${workspaceCreds.agent}/${sessionID}`",
						"node: LIST url-builder must build exactly /workspace/{ns}/{agent}/{session} with no trailing slash")
				}
			})
		}
	}
}

// TestRender_Workspace_WarningsKey pins the interpreter template's turn
// reply wire key doc 14's PR-2 section calls for ("a failed sync is
// reported in the turn result ... surfaces as a structured warning field")
// -- unlike PR-1's exec-contract keys this has no shared struct either, so
// the rendered text is the drift guard. agent.*.tmpl is NOT checked here:
// its helper is opt-in and never wired into a reply of its own.
func TestRender_Workspace_WarningsKey(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"node", "python"} {
		out, _ := render(t, "interpreter", lang)
		assert.Contains(t, string(out), "workspaceWarnings", "%s: missing workspaceWarnings reply key", lang)
	}
}

// TestRender_Workspace_AdditiveOnly pins doc 14's "delete-on-absence off by
// default (workspace is additive; explicit DELETE stays an app call)" rule:
// neither kind's helper block may issue an HTTP DELETE against the workspace
// routes. Scoped to the helper block (not the whole file) so a comment that
// merely MENTIONS "DELETE" in prose (as this block's own doc comment does)
// cannot false-negative the check by coincidence elsewhere in the file.
func TestRender_Workspace_AdditiveOnly(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		for _, lang := range []string{"node", "python"} {
			t.Run(kind+"/"+lang, func(t *testing.T) {
				t.Parallel()
				out, _ := render(t, kind, lang)
				block := workspaceHelperBlock(t, string(out), lang)
				if lang == "python" {
					assert.NotContains(t, block, "requests.delete(",
						"%s/%s: workspace helper must never issue a DELETE (sync is additive-only)", kind, lang)
				} else {
					assert.NotContains(t, block, "method: 'DELETE'",
						"%s/%s: workspace helper must never issue a DELETE (sync is additive-only)", kind, lang)
				}
			})
		}
	}
}

// TestRender_Workspace_SkipsSymlinks pins the review-motivated symlink guard
// in the sync walk: a symlink written into the scratch dir must be SKIPPED,
// not followed and uploaded -- see the trust-boundary comment at the top of
// interpreter.{js,py}.tmpl for why (a link at a pod credential file would
// otherwise get its target's bytes synced to a workspace this agent's
// sibling sessions can read).
func TestRender_Workspace_SkipsSymlinks(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		out, _ := render(t, kind, "python")
		assert.Contains(t, string(out), "os.path.islink(local_path)", "%s/python: sync walk must skip symlinks", kind)

		out, _ = render(t, kind, "node")
		assert.Contains(t, string(out), "entry.isSymbolicLink()", "%s/node: sync walk must skip symlinks", kind)
	}
}

// TestRender_Workspace_SkipCleanUsesManifest pins the "hash manifest kept in
// the scratch dir; second turn on the same warm pod re-syncs nothing when
// nothing changed" requirement: the sync walk must compare against a
// previously recorded manifest entry (size+mtime) before ever falling back
// to a content hash, and the manifest file itself must never be a candidate
// for sync (excluded by name).
func TestRender_Workspace_SkipCleanUsesManifest(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		out, _ := render(t, kind, "python")
		text := string(out)
		assert.Contains(t, text, "WORKSPACE_MANIFEST_FILENAME = '.fission-workspace-manifest.json'", "%s/python: manifest filename constant missing", kind)
		assert.Contains(t, text, "skip-clean: unchanged since the last successful sync/hydrate", "%s/python: skip-clean short-circuit missing", kind)

		out, _ = render(t, kind, "node")
		text = string(out)
		assert.Contains(t, text, "WORKSPACE_MANIFEST_FILENAME = '.fission-workspace-manifest.json'", "%s/node: manifest filename constant missing", kind)
		assert.Contains(t, text, "skip-clean: unchanged since the last successful sync/hydrate", "%s/node: skip-clean short-circuit missing", kind)
	}
}

// TestRender_Workspace_HydrateExcludesManifest pins a review-motivated fix:
// the hydrate loop, like the sync walk (TestRender_Workspace_SkipCleanUsesManifest),
// must never treat the manifest filename as an ordinary workspace artifact --
// a same-named remote artifact would otherwise get downloaded over the local
// manifest every turn and poison skip-clean decisions. Checked in both kinds
// since the byte-identical helper block means a fix to one without the other
// would itself be the regression this guards against.
func TestRender_Workspace_HydrateExcludesManifest(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent", "interpreter"} {
		out, _ := render(t, kind, "python")
		assert.Contains(t, string(out),
			"or rel in (WORKSPACE_MANIFEST_FILENAME, WORKSPACE_MANIFEST_FILENAME + '.tmp')",
			"%s/python: hydrate loop must exclude the manifest filename by name", kind)

		out, _ = render(t, kind, "node")
		assert.Contains(t, string(out),
			"rel === WORKSPACE_MANIFEST_FILENAME ||\n      rel === WORKSPACE_MANIFEST_FILENAME + '.tmp'",
			"%s/node: hydrate loop must exclude the manifest filename by name", kind)
	}
}

// TestRender_Interpreter_Workspace_WiredIntoTurn pins that the interpreter
// template (unlike agent.*.tmpl's opt-in copy) actually CALLS the helper
// from main()/module.exports for a sessionful turn, and gates it on
// is_session -- a stateless call (no session header, or one that failed
// _safe_scratch_name's own charset check) must never hydrate/sync.
func TestRender_Interpreter_Workspace_WiredIntoTurn(t *testing.T) {
	t.Parallel()

	// Call-site literals (not the helper functions' own `def`/`function`
	// signature lines, which are present in BOTH kinds by design -- see
	// TestRender_Workspace_HelperBlockIdenticalAcrossKinds) that only a
	// wired-in caller, not merely a definition, would produce.
	pyHydrateCall := "workspace_warnings += _hydrate_workspace(scratch_dir, session_id, manifest)"
	pySyncCall := "workspace_warnings += _sync_workspace(scratch_dir, session_id, manifest)"
	jsHydrateCall := "workspaceWarnings.concat(await hydrateWorkspace(scratchDir, sessionID, manifest))"
	jsSyncCall := "workspaceWarnings.concat(await syncWorkspace(scratchDir, sessionID, manifest))"

	pyOut, _ := render(t, "interpreter", "python")
	pyText := string(pyOut)
	assert.Contains(t, pyText, "is_session = bool(session_id) and scratch_name == session_id",
		"python: main() must gate hydrate/sync on a validated session id")
	assert.Contains(t, pyText, pyHydrateCall, "python: main() must call _hydrate_workspace at turn start")
	assert.Contains(t, pyText, pySyncCall, "python: main() must call _sync_workspace at turn end")

	jsOut, _ := render(t, "interpreter", "node")
	jsText := string(jsOut)
	assert.Contains(t, jsText, "const isSession = Boolean(sessionID) && scratchName === sessionID;",
		"node: module.exports must gate hydrate/sync on a validated session id")
	assert.Contains(t, jsText, jsHydrateCall, "node: module.exports must call hydrateWorkspace at turn start")
	assert.Contains(t, jsText, jsSyncCall, "node: module.exports must call syncWorkspace at turn end")

	// And the agent.*.tmpl copy must NOT be wired into anything -- it is
	// opt-in only (doc 14's PR-2 Placement note): the helper function
	// DEFINITIONS are present (asserted identical above), but no CALL site
	// invoking them from module-level code should be.
	agentPyOut, _ := render(t, "agent", "python")
	agentPyText := string(agentPyOut)
	assert.NotContains(t, agentPyText, pyHydrateCall, "python: agent.py.tmpl must not call the helper itself -- it is opt-in")
	assert.NotContains(t, agentPyText, pySyncCall, "python: agent.py.tmpl must not call the helper itself -- it is opt-in")

	agentJsOut, _ := render(t, "agent", "node")
	agentJsText := string(agentJsOut)
	assert.NotContains(t, agentJsText, jsHydrateCall, "node: agent.js.tmpl must not call the helper itself -- it is opt-in")
	assert.NotContains(t, agentJsText, jsSyncCall, "node: agent.js.tmpl must not call the helper itself -- it is opt-in")
}
