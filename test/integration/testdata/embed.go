//go:build integration

// Package testdata provides an embedded filesystem of vendored function
// source code used by Go integration tests. Files were copied from the
// fission/examples repository on first migration of each test that needs them.
package testdata

import "embed"

// FS holds vendored example files. Tests should not access the FS directly;
// use framework.WriteTestData(t, "<path>") to materialize a file under
// t.TempDir for the CLI to consume.
//
// As more bash tests migrate, additional language subtrees (go, misc/...)
// get vendored in here and added to the embed directive below. The `all:`
// prefix ensures dotfiles and underscore-prefixed files (e.g. Python's
// __init__.py) are included.
//
//go:embed all:nodejs all:python
var FS embed.FS
