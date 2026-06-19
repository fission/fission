// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"fmt"
	"regexp"
	"strings"
)

// compilePattern turns a route pattern into a regexp matcher. A pattern with no
// "{" is static and returns (nil, nil) — the dispatcher then string-compares.
//
// A template uses gorilla-compatible syntax: {name} matches a single path
// segment ([^/]+); {name:regexp} matches the supplied regexp, which may span
// "/" (e.g. /bank/{html:[a-zA-Z0-9./]+}). Each variable becomes a named capture
// group, so they're extractable via Vars. Exact patterns are anchored at both
// ends; prefix patterns only at the start. A malformed template (unbalanced
// braces, empty name, or an uncompilable regexp) returns an error rather than
// panicking — the caller (router) rejects the trigger instead.
func compilePattern(pattern string, kind MatchKind) (*regexp.Regexp, error) {
	if !strings.Contains(pattern, "{") {
		return nil, nil // static path
	}
	var b strings.Builder
	b.WriteByte('^')
	rest := pattern
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(regexp.QuoteMeta(rest))
			break
		}
		b.WriteString(regexp.QuoteMeta(rest[:i]))
		rest = rest[i+1:]

		// Find the matching '}', tracking nesting so a regexp quantifier like
		// {3} inside {id:[0-9]{3}} doesn't terminate the variable early.
		depth, j := 1, 0
		for ; j < len(rest); j++ {
			if rest[j] == '{' {
				depth++
			} else if rest[j] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if depth != 0 {
			return nil, fmt.Errorf("httpmux: unbalanced '{' in pattern %q", pattern)
		}
		spec := rest[:j]
		rest = rest[j+1:]

		name, expr, hasExpr := strings.Cut(spec, ":")
		if name == "" {
			return nil, fmt.Errorf("httpmux: empty variable name in pattern %q", pattern)
		}
		if !hasExpr {
			expr = "[^/]+" // bare {name}: one path segment, like gorilla
		}
		b.WriteString("(?P<")
		b.WriteString(name)
		b.WriteString(">")
		b.WriteString(expr)
		b.WriteString(")")
	}
	if kind == Exact {
		b.WriteByte('$')
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("httpmux: invalid template %q: %w", pattern, err)
	}
	return re, nil
}

// CompilePattern reports whether a route pattern is valid — a static path or a
// well-formed {var}/{var:regexp} template. The router uses it to reject
// malformed HTTPTrigger paths up front, replacing gorilla's panic-on-bad-
// template behaviour with a returned error.
func CompilePattern(pattern string, kind MatchKind) error {
	_, err := compilePattern(pattern, kind)
	return err
}
