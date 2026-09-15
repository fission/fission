// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package stateapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "fnstate/ns-a/fn-a/agentlog/s-1", StreamName("ns-a", "fn-a", "agentlog/s-1"))
}

func TestValidStream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stream string
		want   bool
	}{
		{"simple", "agentlog", true},
		{"nested path", "agentlog/s-1", true},
		{"deeply nested", "a/b/c/d", true},
		{"dots and underscores allowed", "agent_log.v1/s-1", true},
		{"empty", "", false},
		{"leading slash", "/agentlog", false},
		{"trailing slash", "agentlog/", false},
		{"double slash", "a//b", false},
		{"embedded dots valid", "a..b", true},
		{"dot-dot segment", "../x", false},
		{"dot-dot in middle", "a/../b", false},
		{"dot-dot trailing", "a/..", false},
		{"dot segment", "./x", false},
		{"dot in middle", "a/./b", false},
		{"dot trailing", "a/.", false},
		{"bare dot", ".", false},
		{"exactly 200 chars", strings.Repeat("a", 200), true},
		{"201 chars", strings.Repeat("a", 201), false},
		{"disallowed char space", "agent log", false},
		{"disallowed char at", "agent@log", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidStream(tt.stream))
		})
	}
}
