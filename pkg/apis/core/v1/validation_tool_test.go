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
		{"disabled is inert even if empty", ToolConfig{ExposeAsMCP: false}, false},
		{"disabled with bad schema still inert", ToolConfig{ExposeAsMCP: false, InputSchema: rawSchema(`not json`)}, false},
		{"exposed requires description", ToolConfig{ExposeAsMCP: true}, true},
		{"exposed blank description", ToolConfig{ExposeAsMCP: true, Description: "   "}, true},
		{"exposed with description ok", ToolConfig{ExposeAsMCP: true, Description: "does a thing"}, false},
		{"exposed nil schema ok", ToolConfig{ExposeAsMCP: true, Description: "d"}, false},
		{"exposed empty-raw schema ok", ToolConfig{ExposeAsMCP: true, Description: "d", InputSchema: rawSchema("")}, false},
		{"exposed valid object schema", ToolConfig{ExposeAsMCP: true, Description: "d", InputSchema: rawSchema(`{"type":"object","properties":{"q":{"type":"string"}}}`)}, false},
		{"exposed schema not an object", ToolConfig{ExposeAsMCP: true, Description: "d", InputSchema: rawSchema(`["type"]`)}, true},
		{"exposed schema missing type", ToolConfig{ExposeAsMCP: true, Description: "d", InputSchema: rawSchema(`{"properties":{}}`)}, true},
		{"exposed schema not json", ToolConfig{ExposeAsMCP: true, Description: "d", InputSchema: rawSchema(`{bad`)}, true},
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
		spec.Tool = &ToolConfig{ExposeAsMCP: true} // missing description
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for exposed tool without description")
		}
	})
}
