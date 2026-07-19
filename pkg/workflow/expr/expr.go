// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package expr pins the single JSONPath dialect used by RFC-0022 workflows
// (ojg/jp). Everything that parses or evaluates a workflow JSONPath goes
// through this package so the dialect is a contract, not a library accident.
//
// It must not import pkg/apis: the api package's validation imports this one.
package expr

import (
	"fmt"
	"strings"

	"github.com/ohler55/ojg/jp"
)

// Path is a parsed workflow JSONPath expression.
type Path struct {
	x jp.Expr
}

// Parse compiles a workflow JSONPath. Paths must be absolute ("$"-rooted):
// relative paths are almost always authoring mistakes, and every documented
// example is $-rooted.
func Parse(path string) (Path, error) {
	if !strings.HasPrefix(path, "$") {
		return Path{}, fmt.Errorf("jsonpath %q: must start with $", path)
	}
	x, err := jp.Parse([]byte(path))
	if err != nil {
		return Path{}, fmt.Errorf("jsonpath %q: %w", path, err)
	}
	return Path{x: x}, nil
}
