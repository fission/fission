// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"strings"
	"testing"

	"github.com/fission/fission/pkg/fission-cli/util"
)

func newFissionResources() *FissionResources {
	return &FissionResources{
		SourceMap: SourceMap{Locations: make(map[string](map[string](map[string]Location)))},
	}
}

func TestParseYamlPerKind(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		count func(fr *FissionResources) int
	}{
		{
			name:  "Function",
			yaml:  "apiVersion: fission.io/v1\nkind: Function\nmetadata:\n  name: hello\n  namespace: default\n",
			count: func(fr *FissionResources) int { return len(fr.Functions) },
		},
		{
			name:  "Package",
			yaml:  "apiVersion: fission.io/v1\nkind: Package\nmetadata:\n  name: hello-pkg\n  namespace: default\n",
			count: func(fr *FissionResources) int { return len(fr.Packages) },
		},
		{
			name:  "Environment",
			yaml:  "apiVersion: fission.io/v1\nkind: Environment\nmetadata:\n  name: nodejs\n  namespace: default\n",
			count: func(fr *FissionResources) int { return len(fr.Environments) },
		},
		{
			name:  "HTTPTrigger",
			yaml:  "apiVersion: fission.io/v1\nkind: HTTPTrigger\nmetadata:\n  name: ht\n  namespace: default\n",
			count: func(fr *FissionResources) int { return len(fr.HttpTriggers) },
		},
		{
			name:  "TimeTrigger",
			yaml:  "apiVersion: fission.io/v1\nkind: TimeTrigger\nmetadata:\n  name: tt\n  namespace: default\n",
			count: func(fr *FissionResources) int { return len(fr.TimeTriggers) },
		},
		{
			name:  "MessageQueueTrigger",
			yaml:  "apiVersion: fission.io/v1\nkind: MessageQueueTrigger\nmetadata:\n  name: mqt\n  namespace: default\n",
			count: func(fr *FissionResources) int { return len(fr.MessageQueueTriggers) },
		},
		{
			name:  "KubernetesWatchTrigger",
			yaml:  "apiVersion: fission.io/v1\nkind: KubernetesWatchTrigger\nmetadata:\n  name: kw\n  namespace: default\n",
			count: func(fr *FissionResources) int { return len(fr.KubernetesWatchTriggers) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := newFissionResources()
			if err := fr.ParseYaml([]byte(tt.yaml), &Location{Path: "test.yaml", Line: 1}, ""); err != nil {
				t.Fatalf("ParseYaml(%s) error: %v", tt.name, err)
			}
			if n := tt.count(fr); n != 1 {
				t.Fatalf("expected 1 %s parsed, got %d", tt.name, n)
			}
			// the resource must be tracked in the source map under its kind
			if _, ok := fr.SourceMap.Locations[tt.name]; !ok {
				t.Fatalf("%s not recorded in source map", tt.name)
			}
		})
	}
}

func TestParseYamlUnknownKindIsIgnored(t *testing.T) {
	fr := newFissionResources()
	err := fr.ParseYaml([]byte("apiVersion: v1\nkind: Sandwich\nmetadata:\n  name: blt\n"), &Location{Path: "x.yaml", Line: 1}, "")
	if err != nil {
		t.Fatalf("unknown kind should be ignored, got error: %v", err)
	}
}

func TestParseYamlDuplicateIsRejected(t *testing.T) {
	fr := newFissionResources()
	doc := "apiVersion: fission.io/v1\nkind: Function\nmetadata:\n  name: dup\n  namespace: default\n"
	if err := fr.ParseYaml([]byte(doc), &Location{Path: "a.yaml", Line: 1}, ""); err != nil {
		t.Fatalf("first parse: %v", err)
	}
	err := fr.ParseYaml([]byte(doc), &Location{Path: "b.yaml", Line: 1}, "")
	if err == nil || !strings.Contains(err.Error(), "Duplicate") {
		t.Fatalf("expected duplicate error, got: %v", err)
	}
}

func TestParseYamlAppliesCommitLabel(t *testing.T) {
	fr := newFissionResources()
	doc := "apiVersion: fission.io/v1\nkind: Function\nmetadata:\n  name: labeled\n  namespace: default\n"
	if err := fr.ParseYaml([]byte(doc), &Location{Path: "a.yaml", Line: 1}, "abc123"); err != nil {
		t.Fatal(err)
	}
	if len(fr.Functions) != 1 {
		t.Fatalf("expected 1 function, got %d", len(fr.Functions))
	}
	if got := fr.Functions[0].Labels[util.COMMIT_LABEL]; got != "abc123" {
		t.Fatalf("expected commit label %q=abc123, got %q", util.COMMIT_LABEL, got)
	}
}
