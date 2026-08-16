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
		// The MCP SDK's AddTool panics on a non-object schema; admission must
		// refuse it so one Function can't crash-loop the MCP reconciler.
		{"schema type not object", ToolConfig{Description: "d", InputSchema: rawSchema(`{"type":"string"}`)}, true},
		{"schema type not a string", ToolConfig{Description: "d", InputSchema: rawSchema(`{"type":["object","null"]}`)}, true},
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

// TestToolConfigOnAdmissionPath pins that ToolConfig rules bind kubectl and
// GitOps writers, not only the CLI: the webhook calls
// Function.ValidateForAdmission (NOT Validate), and the MCP SDK panics on a
// schema it dislikes, so a rule reachable only via Validate() would leave the
// reconciler exposed to any non-CLI writer.
func TestToolConfigOnAdmissionPath(t *testing.T) {
	t.Parallel()
	bad := &Function{Spec: FunctionSpec{Tool: &ToolConfig{
		Description: "d",
		InputSchema: rawSchema(`{"type":"string"}`),
	}}}
	err := bad.ValidateForAdmission()
	if err == nil {
		t.Fatal("ValidateForAdmission must reject a non-object tool input schema (this is the webhook's path)")
	}
	good := &Function{Spec: FunctionSpec{Tool: &ToolConfig{
		Description: "d",
		InputSchema: rawSchema(`{"type":"object"}`),
	}}}
	if err := good.ValidateForAdmission(); err != nil {
		t.Fatalf("valid tool config must pass admission: %v", err)
	}
}
