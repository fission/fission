/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package function

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
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

// TestFunctionListOutput exercises the command-level -o wiring (flag -> format
// -> printer) end to end against a fake clientset, complementing the printer
// unit tests in pkg/fission-cli/util. cmd.SetClientset is a sync.Once, so the
// fake set here is shared by the subtests below.
func TestFunctionListOutput(t *testing.T) {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "listfn", Namespace: "default"},
		Spec:       fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
	}
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fn),
		Namespace:        "default",
	})

	t.Run("json is an array containing the function", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.Output, "json")
		out := captureStdout(t, func() error { return List(in) })

		var got []fv1.Function
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not a JSON array: %v\n%s", err, out)
		}
		if len(got) != 1 || got[0].Name != "listfn" {
			t.Fatalf("unexpected functions in json: %+v", got)
		}
	})

	t.Run("wide adds the AGE column", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.Output, "wide")
		out := captureStdout(t, func() error { return List(in) })
		if !strings.Contains(out, "AGE") {
			t.Fatalf("wide output missing AGE column:\n%s", out)
		}
		if !strings.Contains(out, "listfn") {
			t.Fatalf("wide output missing the function row:\n%s", out)
		}
	})

	t.Run("default table omits AGE", func(t *testing.T) {
		in := dummy.TestFlagSet()
		out := captureStdout(t, func() error { return List(in) })
		if strings.Contains(out, "AGE") {
			t.Fatalf("default table should not include AGE:\n%s", out)
		}
		if !strings.Contains(out, "NAME") || !strings.Contains(out, "listfn") {
			t.Fatalf("default table missing expected content:\n%s", out)
		}
	})

	t.Run("invalid format errors", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.Output, "bogus")
		if err := List(in); err == nil {
			t.Fatal("expected an error for -o bogus")
		}
	})
}
