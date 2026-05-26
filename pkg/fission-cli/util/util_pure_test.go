// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKubifyName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already valid", "hello", "hello"},
		{"uppercase and spaces", "Hello World", "hello-world"},
		{"leading non-alpha trimmed", "123-abc", "abc"},
		{"trailing non-alnum trimmed", "abc--", "abc"},
		{"all invalid becomes default", "___", "default"},
		{"underscores replaced", "a_b_c", "a-b-c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, KubifyName(tt.in))
		})
	}

	t.Run("truncated to 63 chars", func(t *testing.T) {
		t.Parallel()
		assert.Len(t, KubifyName(strings.Repeat("a", 100)), 63)
	})
}

func TestUpdateMapFromStringSlice(t *testing.T) {
	t.Parallel()
	m := map[string]string{}
	updated := UpdateMapFromStringSlice(&m, []string{"a=1", "b=2", "novalue"})
	assert.True(t, updated)
	assert.Equal(t, map[string]string{"a": "1", "b": "2"}, m)

	empty := map[string]string{}
	assert.False(t, UpdateMapFromStringSlice(&empty, []string{"nokv"}), "no valid key=value pairs means no update")
}

func TestGetFissionNamespace(t *testing.T) {
	t.Setenv(ENV_FISSION_NAMESPACE, "fission-system")
	assert.Equal(t, "fission-system", GetFissionNamespace())
}

func TestResolveFunctionNS(t *testing.T) {
	t.Run("non-default namespace returned as-is", func(t *testing.T) {
		assert.Equal(t, "team-a", ResolveFunctionNS("team-a"))
	})

	t.Run("default falls back to env override", func(t *testing.T) {
		t.Setenv(ENV_FUNCTION_NAMESPACE, "fn-ns")
		assert.Equal(t, "fn-ns", ResolveFunctionNS(metav1.NamespaceDefault))
	})

	t.Run("default with no override stays default", func(t *testing.T) {
		t.Setenv(ENV_FUNCTION_NAMESPACE, "")
		assert.Equal(t, metav1.NamespaceDefault, ResolveFunctionNS(metav1.NamespaceDefault))
	})
}
