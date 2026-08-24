// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"os"
	"strings"
	"testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	if err := fn(); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestAgentListOutput exercises `fission agent list` against a fake clientset:
// only the function with FunctionSpec.Agent set is listed, and unset fields
// render as their documented defaults.
func TestAgentListOutput(t *testing.T) {
	// A non-"default" namespace keeps the NAMESPACE column from accidentally
	// satisfying the "nil duration renders as default" assertion below.
	const ns = "testns"
	plainFn := &fv1.Function{
		Name: "plainfn", Namespace: ns,
		Spec: fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
	}
	agentFn := &fv1.Function{
		Name: "agentfn", Namespace: ns,
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "nodejs"},
			Agent:       &fv1.AgentConfig{},
		},
	}
	// Reset the sync.Once-guarded client so this test deterministically
	// installs its own fake regardless of test order.
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(plainFn, agentFn),
		Namespace:        ns,
	})
	t.Cleanup(cmd.ResetClientsetForTest)

	in := dummy.TestFlagSet()
	out := captureStdout(t, func() error { return List(in) })

	if strings.Contains(out, "plainfn") {
		t.Fatalf("non-agent function should not be listed:\n%s", out)
	}
	if !strings.Contains(out, "agentfn") {
		t.Fatalf("agent function missing from output:\n%s", out)
	}
	if !strings.Contains(out, "header/X-Fission-Session") {
		t.Fatalf("nil Session should render as header/X-Fission-Session:\n%s", out)
	}
	if !strings.Contains(out, "default") {
		t.Fatalf("nil IdleAfter/ArchiveAfter should render as default:\n%s", out)
	}
	if !strings.Contains(out, "unlimited") {
		t.Fatalf("zero MaxSessions should render as unlimited:\n%s", out)
	}
}
