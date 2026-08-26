// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// render is a small helper: render lang's template and require success.
func render(t *testing.T, lang string) ([]byte, string) {
	t.Helper()
	out, ext, err := Render(lang, "widget")
	require.NoError(t, err)
	require.NotEmpty(t, out)
	return out, ext
}

func TestRender_UnsupportedLanguage(t *testing.T) {
	t.Parallel()
	_, _, err := Render("ruby", "widget")
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
	for _, lang := range []string{"node", "python"} {
		for _, tc := range cases {
			t.Run(lang+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				out, _, err := Render(lang, tc.payload)
				require.Error(t, err, "Render must reject unsafe name %q", tc.payload)
				assert.Empty(t, out, "Render must not return partial output on a rejected name")
			})
		}
	}
}

func TestRender_Extensions(t *testing.T) {
	t.Parallel()
	_, ext := render(t, "node")
	assert.Equal(t, "js", ext)
	_, ext = render(t, "python")
	assert.Equal(t, "py", ext)
}

func TestRender_NoTemplateArtifacts(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"node", "python"} {
		out, _ := render(t, lang)
		text := string(out)
		assert.NotContains(t, text, "{{", "%s template left an unrendered placeholder", lang)
		// render(t, lang) passes name "widget" -- confirm substitution
		// actually ran, not merely that the placeholder syntax is gone.
		assert.Contains(t, text, "widget", "%s: Name substitution did not run", lang)
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
			out, _ := render(t, tc.lang)
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
		out, _ := render(t, lang)
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
		out, _ := render(t, lang)
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
		out, _ := render(t, lang)
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
func TestRender_YieldDefaultsToWaiting(t *testing.T) {
	t.Parallel()

	for _, lang := range []string{"node", "python"} {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			out, _ := render(t, lang)
			text := string(out)
			prefix := commentPrefix(lang)

			var activeWaiting bool
			for _, line := range strings.Split(text, "\n") {
				trimmed := strings.TrimSpace(line)
				isCode := trimmed != "" && !strings.HasPrefix(trimmed, prefix)

				if strings.Contains(line, agentruntime.YieldContinue) {
					// "continue" must never appear in the ACTIVE code path
					// (a scaffold that yielded it by default would
					// self-re-invoke forever on a fresh install).
					assert.False(t, isCode,
						"%s: %q value found outside a comment: %q", lang, agentruntime.YieldContinue, line)
				}
				if isCode && strings.Contains(line, agentruntime.YieldWaiting) {
					activeWaiting = true
				}
			}
			assert.True(t, activeWaiting,
				"%s: no active (non-comment) line yields %q", lang, agentruntime.YieldWaiting)
		})
	}
}

// TestRender_NodeSyntax runs `node --check` against the rendered Node
// template. Skipped (with a note) when node is not on PATH.
func TestRender_NodeSyntax(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH; skipping node --check")
	}

	out, _ := render(t, "node")
	path := t.TempDir() + "/agent.js"
	require.NoError(t, os.WriteFile(path, out, 0o600))

	cmd := exec.Command(nodeBin, "--check", path)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "node --check failed: %s", output)
}

// TestRender_PythonSyntax runs `python3 -m py_compile` against the rendered
// Python template. Skipped (with a note) when python3 is not on PATH.
func TestRender_PythonSyntax(t *testing.T) {
	pyBin, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found on PATH; skipping py_compile")
	}

	out, _ := render(t, "python")
	path := t.TempDir() + "/agent.py"
	require.NoError(t, os.WriteFile(path, out, 0o600))

	cmd := exec.Command(pyBin, "-m", "py_compile", path)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "python3 -m py_compile failed: %s", output)
}
