// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func rawSchema(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

func TestToolConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     ToolConfig
		wantErr bool
	}{
		{"requires description", ToolConfig{}, true},
		{"blank description", ToolConfig{Description: "   "}, true},
		{"with description ok", ToolConfig{Description: "does a thing"}, false},
		{"nil schema ok", ToolConfig{Description: "d"}, false},
		{"empty-raw schema ok", ToolConfig{Description: "d", InputSchema: rawSchema("")}, false},
		{"valid object schema", ToolConfig{Description: "d", InputSchema: rawSchema(`{"type":"object","properties":{"q":{"type":"string"}}}`)}, false},
		{"schema not an object", ToolConfig{Description: "d", InputSchema: rawSchema(`["type"]`)}, true},
		{"schema missing type", ToolConfig{Description: "d", InputSchema: rawSchema(`{"properties":{}}`)}, true},
		{"schema not json", ToolConfig{Description: "d", InputSchema: rawSchema(`{bad`)}, true},
		// RFC-0025: Alias is a format-only kube-name check (no existence check
		// — aliases are eventually consistent).
		{"valid alias ok", ToolConfig{Description: "d", Alias: "prod"}, false},
		{"malformed alias rejected", ToolConfig{Description: "d", Alias: "Bad_Alias"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestFunctionSpecValidateTool asserts FunctionSpec.Validate surfaces a bad Tool
// config and that a nil Tool is accepted (backward-compat guard).
func TestFunctionSpecValidateTool(t *testing.T) {
	t.Parallel()

	base := func() FunctionSpec {
		return FunctionSpec{
			Environment: EnvironmentReference{Name: "env", Namespace: "default"},
		}
	}

	t.Run("nil tool ok", func(t *testing.T) {
		t.Parallel()
		spec := base()
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid tool surfaced", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Tool = &ToolConfig{} // missing description
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for exposed tool without description")
		}
	})

	t.Run("invalid tool alias surfaced", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Tool = &ToolConfig{Description: "d", Alias: "Bad_Alias"}
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for malformed tool alias")
		}
	})
}
