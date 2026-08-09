// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/gengo/v2/types"
)

// fixedNamer is a namer.Namer that always returns the same string, so the test
// exercises keywordSafeNamer's decision rather than gengo's lowercasing.
type fixedNamer struct{ out string }

func (f fixedNamer) Name(*types.Type) string { return f.out }

func TestKeywordSafeNamer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"Package collides and gains a trailing underscore", "package", "package_"},
		{"Function does not collide and is untouched", "function", "function"},
		{"type is a keyword too", "type", "type_"},
		{"interface is a keyword too", "interface", "interface_"},
		{"an already-suffixed name is left alone", "package_", "package_"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := keywordSafeNamer{fixedNamer{tc.in}}.Name(nil)

			assert.Equal(t, tc.want, got)
			// The private name also becomes the generated file name, and the Go
			// toolchain ignores files whose names begin with "_". A leading
			// underscore would make the generated client silently invisible.
			assert.False(t, strings.HasPrefix(got, "_"),
				"the keyword suffix must trail: a leading underscore yields _%s.go, which Go ignores", tc.in)
		})
	}
}
