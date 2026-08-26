// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package templates embeds the handler files `fission agent create`
// scaffolds (G17 DX slice, see docs/superpowers/plans/2026-08-26-agent-runtime-scaffold.md
// Task 1). Render fills in the one placeholder every template carries (the
// scaffolded function's name, used only in a header comment and in a couple
// of log-line prefixes) and returns the rendered bytes plus the file
// extension the caller should write them under.
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
)

//go:embed agent.js.tmpl
var nodeTemplateSrc string

//go:embed agent.py.tmpl
var pythonTemplateSrc string

// templateData is the substitution set every template receives. Name is the
// scaffolded function's name; it appears only in a header comment and a
// couple of log-line prefixes, never in anything the wire contract checks.
type templateData struct {
	Name string
}

// langTemplate pairs an embedded template source with the file extension
// (no leading dot) the rendered handler should be written under.
type langTemplate struct {
	src string
	ext string
}

// languages is the supported `--lang` values. Keyed by the same lowercase
// strings the CLI flag accepts.
var languages = map[string]langTemplate{
	"node":   {src: nodeTemplateSrc, ext: "js"},
	"python": {src: pythonTemplateSrc, ext: "py"},
}

// Render renders the embedded handler template for lang ("node" or
// "python") with name substituted in, and returns the rendered bytes plus
// the file extension (no leading dot) the caller should write them under.
func Render(lang, name string) ([]byte, string, error) {
	lt, ok := languages[lang]
	if !ok {
		return nil, "", fmt.Errorf("unsupported agent handler language %q (want \"node\" or \"python\")", lang)
	}
	tmpl, err := template.New(lang).Parse(lt.src)
	if err != nil {
		return nil, "", fmt.Errorf("parsing %s handler template: %w", lang, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData{Name: name}); err != nil {
		return nil, "", fmt.Errorf("rendering %s handler template: %w", lang, err)
	}
	return buf.Bytes(), lt.ext, nil
}
