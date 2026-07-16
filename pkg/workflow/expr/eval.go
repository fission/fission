// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package expr

import (
	"fmt"

	"github.com/ohler55/ojg/alt"
)

// Get evaluates the path against doc (a decoded JSON tree). Multiple matches
// pin the FIRST; an explicit null value IS a match. A no-match returns
// (nil, false) — RFC-0022 maps it to JSON null on read paths.
func (p Path) Get(doc any) (any, bool) {
	if vals := p.x.Get(doc); len(vals) > 0 {
		return vals[0], true
	}
	// Get skips explicit nulls; Has distinguishes "matched null" from "no
	// match" so the two are not conflated.
	if p.x.Has(doc) {
		return nil, true
	}
	return nil, false
}

// SetResult writes result at the path into doc and returns the new document,
// never mutating the input. Step Functions parity: missing map parents are
// auto-created (resultPath $.charge works on any input); a genuinely
// unwritable path (wildcard/filter, descending through a scalar) is an
// error — RFC-0022 maps it to Fission.InvalidPath, because silently dropping
// a result is the worst possible default.
func (p Path) SetResult(doc, result any) (any, error) {
	if len(p.x) <= 1 { // bare "$": replace the whole document
		return result, nil
	}
	out := alt.Dup(doc)
	if err := p.x.Set(out, result); err != nil {
		return nil, fmt.Errorf("jsonpath %q: cannot write result: %w", p.x.String(), err)
	}
	return out, nil
}
