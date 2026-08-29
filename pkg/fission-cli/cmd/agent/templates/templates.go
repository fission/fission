// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package templates embeds the handler files `fission agent create`
// scaffolds (G17 DX slice, see docs/superpowers/plans/2026-08-26-agent-runtime-scaffold.md
// Task 1). Render fills in the templateData every template carries -- the
// scaffolded function's name (used only in a header comment and in a couple
// of log-line prefixes) plus, for the interpreter templates only, the
// MAX_TIMEOUT_SECONDS ceiling (see InterpreterMaxTimeoutSeconds) -- and
// returns the rendered bytes plus the file extension the caller should
// write them under.
//
// Why .tmpl, not .js/.py: the Makefile's LICENSE_FILES glob (`make license`
// / `make license-check`, see Makefile's "License headers" section) matches
// `*.py` and `*.go` and does NOT prune this templates directory, so a raw
// `agent.py` sitting here would be treated as an in-repo Fission source file
// missing an SPDX header. These are not Fission source — they are USER code
// written into someone else's project by the scaffold — so they must never
// carry an SPDX header. `.tmpl` is outside the glob's extension list, which
// is exactly the exemption we want; this file (templates.go) is the actual
// Fission source here and DOES carry the header above.
//
// Python handler contract (verify-first, per plan review -- no in-repo
// fixture shows Flask header access or a header-carrying response, unlike
// the Node handler shape which test/integration/testdata/nodejs/* and
// demo/agent-boardroom/fixtures/support-desk.js already demonstrate):
//   - def main() taking no arguments is the entrypoint Fission's Python
//     environment calls: github.com/fission/environments python/server.py,
//     FuncApp._load_v2 resolves the handler symbol and FuncApp.userfunc_call
//     invokes it as self.userfunc(*args) with no positional args for the
//     '/' route.
//   - Header access is Flask's global `request` proxy:
//     github.com/fission/examples python/requestdata.py
//     ("from flask import request" ... "request.headers").
//   - A response can be a (body, status) tuple:
//     github.com/fission/examples python/statuscode.py ("return \"Not
//     Found\\n\", 404"). The three-element (body, status, headers) form
//     needed to set the yield header is Flask's own documented contract, not
//     something either fixture exercises:
//     https://flask.palletsprojects.com/en/stable/quickstart/#about-responses
//     -- "If a tuple is returned the items in the tuple can provide extra
//     information. Such tuples have to be in the form (response, status),
//     (response, headers), or (response, status, headers)."
//   - requests (github.com/fission/environments python/requirements.txt)
//     is present in the image, so the template uses it rather than urllib.
package templates

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

//go:embed agent.js.tmpl
var nodeAgentTemplateSrc string

//go:embed agent.py.tmpl
var pythonAgentTemplateSrc string

// interpreter.{js,py}.tmpl (PR-1, abstraction A / Q35 resolution): the
// stateless exec verb -- code/command in, stdout/stderr/exit out -- see
// their own doc comments for the wire contract.
//
//go:embed interpreter.js.tmpl
var nodeInterpreterTemplateSrc string

//go:embed interpreter.py.tmpl
var pythonInterpreterTemplateSrc string

// workspace_helper.{js,py}.tmpl hold the workspace hydrate/sync helper block
// each kind's template includes via {{template "workspaceHelper" .}}. One
// physical copy per language: interpreter.*.tmpl wires the helper into its
// exec turn, agent.*.tmpl carries it as an opt-in call, and both render the
// IDENTICAL block because they include the same fragment -- the cross-kind
// byte-identity that used to rest on a regression test alone is now
// structural (the test remains as a tripwire on the include sites).
//
//go:embed workspace_helper.js.tmpl
var nodeWorkspaceHelperSrc string

//go:embed workspace_helper.py.tmpl
var pythonWorkspaceHelperSrc string

// InterpreterMaxTimeoutSeconds is the interpreter template's own hard
// ceiling on the request-carried `timeoutSeconds` (rendered into both
// interpreter templates as MAX_TIMEOUT_SECONDS) and the single source of
// truth `fission agent create` uses to set the scaffolded Function's own
// FunctionTimeout to match -- see create.go's buildFunction. Exported so
// that caller stays in lockstep with the templates instead of carrying an
// independent literal.
const InterpreterMaxTimeoutSeconds = 300

// templateData is the substitution set every template receives. Name is the
// scaffolded function's name; it appears only in a header comment and a
// couple of log-line prefixes, never in anything the wire contract checks.
// MaxTimeoutSeconds is only referenced by the interpreter templates (see
// InterpreterMaxTimeoutSeconds).
type templateData struct {
	Name              string
	MaxTimeoutSeconds int
}

// langTemplate pairs an embedded template source with the file extension
// (no leading dot) the rendered handler should be written under, plus the
// language's shared workspace-helper fragment (see the embed comment above)
// associated into the template set at parse time.
type langTemplate struct {
	src    string
	helper string
	ext    string
}

// kinds is the supported `--template` x `--lang` matrix. Keyed by the same
// lowercase strings the CLI flags accept.
var kinds = map[string]map[string]langTemplate{
	"agent": {
		"node":   {src: nodeAgentTemplateSrc, helper: nodeWorkspaceHelperSrc, ext: "js"},
		"python": {src: pythonAgentTemplateSrc, helper: pythonWorkspaceHelperSrc, ext: "py"},
	},
	"interpreter": {
		"node":   {src: nodeInterpreterTemplateSrc, helper: nodeWorkspaceHelperSrc, ext: "js"},
		"python": {src: pythonInterpreterTemplateSrc, helper: pythonWorkspaceHelperSrc, ext: "py"},
	},
}

// Render renders the embedded handler template for kind ("agent" or
// "interpreter") and lang ("node" or "python") with name substituted in,
// and returns the rendered bytes plus the file extension (no leading dot)
// the caller should write them under.
//
// name is validated against Fission's own Kubernetes-name charset
// (fv1.ValidateKubeName, the same DNS-1123-label check `fn create` et al.
// run against a resource name) BEFORE it is interpolated into either
// template. This is defense-in-depth, not the only gate: Task 2's `agent
// create` handler validates the name too, but it writes the rendered
// handler file to disk BEFORE any k8s-side validation runs (the k8s API
// only rejects an invalid name once the Package/Function object is
// created, several steps later) -- so Render must be safe to call with an
// adversarial name on its own. Every template interpolates {{.Name}} inside
// JS backtick template literals and/or Python %-format single-quoted
// strings (log-line/comment prefixes); an unvalidated name containing a
// backtick or a single quote breaks out of those literals and lands
// arbitrary text in LIVE, non-comment code in the scaffolded file --
// confirmed exploitable in review, not theoretical. A DNS-1123 label
// (lowercase alphanumeric/'-', alphanumeric start/end, <=63 chars) cannot
// contain a backtick, quote, `{{`, or a newline, so validating here closes
// the injection regardless of what any caller does or forgets to do
// upstream.
func Render(kind, lang, name string) ([]byte, string, error) {
	langs, ok := kinds[kind]
	if !ok {
		return nil, "", fmt.Errorf("unsupported agent template %q (want \"agent\" or \"interpreter\")", kind)
	}
	lt, ok := langs[lang]
	if !ok {
		return nil, "", fmt.Errorf("unsupported agent handler language %q (want \"node\" or \"python\")", lang)
	}
	if err := fv1.ValidateKubeName("name", name); err != nil {
		return nil, "", fmt.Errorf("invalid agent name %q: %w", name, err)
	}
	tmpl, err := template.New(kind + "/" + lang).Parse(lt.src)
	if err != nil {
		return nil, "", fmt.Errorf("parsing %s/%s handler template: %w", kind, lang, err)
	}
	if _, err := tmpl.New("workspaceHelper").Parse(lt.helper); err != nil {
		return nil, "", fmt.Errorf("parsing %s workspace helper fragment: %w", lang, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData{Name: name, MaxTimeoutSeconds: InterpreterMaxTimeoutSeconds}); err != nil {
		return nil, "", fmt.Errorf("rendering %s/%s handler template: %w", kind, lang, err)
	}
	return buf.Bytes(), lt.ext, nil
}
