# Output Formats (`-o json|yaml|wide`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `-o json|yaml|wide` to the Fission CLI read commands (list + describe), keeping today's table as the no-flag default.

**Architecture:** A format-aware printer in `pkg/fission-cli/util/output.go` sits on top of the existing `PrintTable`/`PrintItems` helpers. List commands route their items through `PrintObjects[T]` (handles table/wide/json/yaml); describe commands call `PrintStructured` (json/yaml) and fall back to their existing human rendering for table/wide. A new optional `--output`/`-o` flag carries the value.

**Tech Stack:** Go 1.26 generics, `encoding/json`, `sigs.k8s.io/yaml` (already a dep), `k8s.io/apimachinery/pkg/util/duration` (already used for AGE).

**Design doc:** `docs/cli-refactor/2026-05-25-output-formats-design.md`. Refinements adopted here: JSON/YAML marshal the slice directly (JSON array / YAML sequence), wide columns are appended at the end (AGE last), and a pure `encode()` helper makes structured output unit-testable.

---

## File structure

- `pkg/fission-cli/util/output.go` — add `OutputFormat`, `ParseOutputFormat`, `encode`, `PrintObjects[T]`, `PrintStructured`. (Existing `PrintTable`/`PrintItems`/`NewTabWriter` stay.)
- `pkg/fission-cli/util/output_test.go` — unit tests for the new helpers.
- `pkg/fission-cli/flag/key/key.go` — no change; the read commands reuse the existing `Output = "output"` key.
- `pkg/fission-cli/flag/flag.go` — `Output` flag var.
- `pkg/fission-cli/cmd/<res>/command.go` — add `flag.Output` to the in-scope subcommands.
- `pkg/fission-cli/cmd/<res>/list.go` — read `-o`, define wide columns, call `PrintObjects`.
- `pkg/fission-cli/cmd/function/getmeta.go`, `package/info.go`, `package/util/util.go` (`PrintPackageSummary`), `canaryconfig/get.go`, `httptrigger/get.go` — `PrintStructured` early-return for json/yaml.

---

### Task 1: Output format type, parsing, and structured encoder

**Files:**
- Modify: `pkg/fission-cli/util/output.go`
- Test: `pkg/fission-cli/util/output_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `output_test.go`:

```go
func TestParseOutputFormat(t *testing.T) {
	tests := []struct {
		in      string
		want    OutputFormat
		wantErr bool
	}{
		{"", OutputTable, false},
		{"wide", OutputWide, false},
		{"json", OutputJSON, false},
		{"yaml", OutputYAML, false},
		{"JSON", OutputJSON, false}, // case-insensitive
		{"name", "", true},
		{"xml", "", true},
	}
	for _, tt := range tests {
		got, err := ParseOutputFormat(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseOutputFormat(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Errorf("ParseOutputFormat(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEncode(t *testing.T) {
	items := []map[string]string{{"name": "a"}, {"name": "b"}}

	j, err := encode(OutputJSON, items)
	if err != nil {
		t.Fatal(err)
	}
	var back []map[string]string
	if err := json.Unmarshal(j, &back); err != nil {
		t.Fatalf("json did not round-trip: %v\n%s", err, j)
	}
	if len(back) != 2 || back[0]["name"] != "a" {
		t.Fatalf("unexpected json: %s", j)
	}

	y, err := encode(OutputYAML, items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(y), "name: a") {
		t.Fatalf("unexpected yaml: %s", y)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/fission-cli/util/ -run 'TestParseOutputFormat|TestEncode' -v`
Expected: FAIL — `undefined: ParseOutputFormat`, `undefined: encode`, `undefined: OutputTable` (and add `encoding/json` import to the test).

- [ ] **Step 3: Implement the type, parser, and encoder**

Add to `output.go` (imports: add `encoding/json`, `sigs.k8s.io/yaml`):

```go
// OutputFormat is the validated value of the -o/--output flag.
type OutputFormat string

const (
	OutputTable OutputFormat = ""     // default human table
	OutputWide  OutputFormat = "wide" // table + extra columns
	OutputJSON  OutputFormat = "json"
	OutputYAML  OutputFormat = "yaml"
)

// ParseOutputFormat validates the -o value (empty is allowed and means table).
func ParseOutputFormat(s string) (OutputFormat, error) {
	switch f := OutputFormat(strings.ToLower(s)); f {
	case OutputTable, OutputWide, OutputJSON, OutputYAML:
		return f, nil
	default:
		return "", fmt.Errorf("invalid output format %q: valid values are wide, json, yaml", s)
	}
}

// encode marshals v as JSON or YAML. It is the structured half of the printer,
// kept pure (no stdout) so it is straightforward to unit test.
func encode(format OutputFormat, v any) ([]byte, error) {
	switch format {
	case OutputJSON:
		return json.MarshalIndent(v, "", "  ")
	case OutputYAML:
		return yaml.Marshal(v)
	default:
		return nil, fmt.Errorf("encode called with non-structured format %q", format)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/fission-cli/util/ -run 'TestParseOutputFormat|TestEncode' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w pkg/fission-cli/util/output.go pkg/fission-cli/util/output_test.go
git add pkg/fission-cli/util/output.go pkg/fission-cli/util/output_test.go
git commit -m "fission-cli/util: add OutputFormat parsing + structured encoder"
```

---

### Task 2: `PrintObjects` (list) and `PrintStructured` (describe)

**Files:**
- Modify: `pkg/fission-cli/util/output.go`
- Test: `pkg/fission-cli/util/output_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestPrintObjectsTableAndWide(t *testing.T) {
	type row struct{ name, ready, age string }
	items := []row{{"a", "True", "5m"}, {"b", "<none>", "1h"}}
	hdr := []string{"NAME", "READY"}
	rowFn := func(r row) []string { return []string{r.name, r.ready} }
	wideHdr := []string{"AGE"}
	wideFn := func(r row) []string { return []string{r.age} }

	// table: no AGE column
	out := captureStdout(t, func() error { return PrintObjects(OutputTable, items, hdr, rowFn, wideHdr, wideFn) })
	if strings.Contains(out, "AGE") {
		t.Errorf("table output must not include wide columns:\n%s", out)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "a") {
		t.Errorf("table missing base columns:\n%s", out)
	}

	// wide: AGE present
	out = captureStdout(t, func() error { return PrintObjects(OutputWide, items, hdr, rowFn, wideHdr, wideFn) })
	if !strings.Contains(out, "AGE") || !strings.Contains(out, "5m") {
		t.Errorf("wide output missing AGE:\n%s", out)
	}

	// json: array round-trips
	out = captureStdout(t, func() error { return PrintObjects(OutputJSON, items, hdr, rowFn, wideHdr, wideFn) })
	var back []row // unexported fields won't unmarshal; just assert it's a JSON array
	_ = back
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("json output should be an array:\n%s", out)
	}
}

func TestPrintStructured(t *testing.T) {
	obj := map[string]string{"name": "hello"}

	// table/wide: not handled, no output
	out := captureStdout(t, func() error {
		handled, err := PrintStructured(OutputTable, obj)
		if handled {
			t.Error("table must not be handled by PrintStructured")
		}
		return err
	})
	if out != "" {
		t.Errorf("expected no output for table, got %q", out)
	}

	// yaml: handled, prints
	out = captureStdout(t, func() error {
		handled, err := PrintStructured(OutputYAML, obj)
		if !handled {
			t.Error("yaml must be handled")
		}
		return err
	})
	if !strings.Contains(out, "name: hello") {
		t.Errorf("yaml not printed:\n%s", out)
	}
}
```

Add a `captureStdout` test helper at the bottom of `output_test.go` (factor it out of the existing `TestPrintItems`, which currently inlines the same os.Pipe dance):

```go
// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it wrote.
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
		t.Fatalf("fn returned error: %v", err)
	}
	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/fission-cli/util/ -run 'TestPrintObjects|TestPrintStructured' -v`
Expected: FAIL — `undefined: PrintObjects`, `undefined: PrintStructured`.

- [ ] **Step 3: Implement `PrintObjects` and `PrintStructured`**

Add to `output.go`:

```go
// PrintObjects renders a slice of items in the requested format. For json/yaml
// it marshals items (a JSON array / YAML sequence). For table it uses
// headers+row; for wide it appends wideExtra columns (e.g. AGE) after the base
// columns. It is the single entry point for the list commands.
func PrintObjects[T any](format OutputFormat, items []T, headers []string, row func(T) []string, wideExtra []string, wideRow func(T) []string) error {
	switch format {
	case OutputJSON, OutputYAML:
		b, err := encode(format, items)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	case OutputWide:
		hdr := append(append([]string{}, headers...), wideExtra...)
		PrintItems(hdr, items, func(t T) []string {
			return append(row(t), wideRow(t)...)
		})
		return nil
	default: // OutputTable
		PrintItems(headers, items, row)
		return nil
	}
}

// PrintStructured prints v as json/yaml and returns true when the format is
// structured; for table/wide it prints nothing and returns false so describe
// commands fall back to their own human rendering.
func PrintStructured(format OutputFormat, v any) (bool, error) {
	switch format {
	case OutputJSON, OutputYAML:
		b, err := encode(format, v)
		if err != nil {
			return true, err
		}
		fmt.Println(string(b))
		return true, nil
	default:
		return false, nil
	}
}
```

Also refactor the existing `TestPrintItems` to use the new `captureStdout` helper (delete its inline os.Pipe block, call `captureStdout`).

- [ ] **Step 4: Run all util tests**

Run: `go test ./pkg/fission-cli/util/ -v`
Expected: PASS (including the refactored `TestPrintItems`).

- [ ] **Step 5: Commit**

```bash
gofmt -w pkg/fission-cli/util/output.go pkg/fission-cli/util/output_test.go
git add pkg/fission-cli/util/output.go pkg/fission-cli/util/output_test.go
git commit -m "fission-cli/util: add PrintObjects + PrintStructured format-aware printers"
```

---

### Task 3: The `--output` flag

**Files:**
- Modify: `pkg/fission-cli/flag/key/key.go`
- Modify: `pkg/fission-cli/flag/flag.go`

- [ ] **Step 1: Add the flag key**

**As implemented:** `key.go` already has `Output = "output"` (aliased by the download keys), so **no new key is added** — the read commands' format flag reuses `flagkey.Output`. The flag *name* `output`/`-o` is shared; only read commands register the `flag.Output` var below, so there is no collision with the download commands' own flags.

- [ ] **Step 2: Add the flag var**

In `flag.go`, add near the other global-ish flags (note: `flagkey.Output`, the existing key — not a new `flagkey.OutputFormat`):

```go
Output = Flag{Type: String, Name: flagkey.Output, Short: "o", Usage: "Output format: wide, json or yaml (default: table)"}
```

- [ ] **Step 3: Verify build**

Run: `go build ./pkg/fission-cli/...`
Expected: builds (flag unused yet is fine — it's a package-level var).

- [ ] **Step 4: Commit**

```bash
git add pkg/fission-cli/flag/key/key.go pkg/fission-cli/flag/flag.go
git commit -m "fission-cli/flag: add --output/-o format flag for read commands"
```

---

### Task 4: Wire `function list` (representative list command)

**Files:**
- Modify: `pkg/fission-cli/cmd/function/command.go` (add `flag.Output` to the list subcommand's Optional flags)
- Modify: `pkg/fission-cli/cmd/function/list.go`
- Test: `pkg/fission-cli/cmd/function/list_test.go` (new)

- [ ] **Step 1: Add the flag to the command**

In `function/command.go`, the `list` subcommand's flag set — add `flag.Output`:

```go
}, List, flag.FlagSet{
	Optional: []flag.Flag{flag.NamespaceFunction, flag.AllNamespaces, flag.Output},
})
```

- [ ] **Step 2: Write the failing test**

Create `function/list_test.go` using the dummy driver + fake fission clientset (follow the pattern in `function/create_test.go` for clientset/`cmd.SetClientset` setup):

```go
func TestFunctionListJSON(t *testing.T) {
	// arrange: a fake clientset with one function, wired via cmd.SetClientset,
	// and a dummy CLI input with output=json (see create_test.go for the helper).
	input := dummy.TestFlagSet()
	input.Set(flagkey.Output, "json")
	out := captureStdout(t, func() error { return List(input) })
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("expected JSON array, got:\n%s", out)
	}
}
```

(Reuse/duplicate the `captureStdout` helper locally, or export a small test util. Mirror the clientset bootstrap already used by `function/create_test.go` / `function_test.go`.)

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./pkg/fission-cli/cmd/function/ -run TestFunctionListJSON -v`
Expected: FAIL — output is the table, not JSON (List ignores `-o`).

- [ ] **Step 4: Implement `-o` in `function/list.go`**

Replace the `util.PrintItems(...)` call with format-aware printing:

```go
format, err := util.ParseOutputFormat(input.String(flagkey.Output))
if err != nil {
	return err
}

headers := []string{"NAME", "ENV", "EXECUTORTYPE", "MINSCALE", "MAXSCALE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "SECRETS", "CONFIGMAPS", "READY", "NAMESPACE"}
wideExtra := []string{"AGE"}
row := func(f fv1.Function) []string {
	var secretsList, configMapList []string
	for _, secret := range f.Spec.Secrets {
		secretsList = append(secretsList, secret.Name)
	}
	for _, configMap := range f.Spec.ConfigMaps {
		configMapList = append(configMapList, configMap.Name)
	}
	return []string{
		f.Name, f.Spec.Environment.Name,
		string(f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType),
		fmt.Sprintf("%v", f.Spec.InvokeStrategy.ExecutionStrategy.MinScale),
		fmt.Sprintf("%v", f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale),
		f.Spec.Resources.Requests.Cpu().String(),
		f.Spec.Resources.Limits.Cpu().String(),
		f.Spec.Resources.Requests.Memory().String(),
		f.Spec.Resources.Limits.Memory().String(),
		strings.Join(secretsList, ","),
		strings.Join(configMapList, ","),
		util.ConditionStatus(f.Status.Conditions, fv1.FunctionConditionReady),
		f.Namespace,
	}
}
wideRow := func(f fv1.Function) []string {
	return []string{util.AgeOf(f.CreationTimestamp)}
}
return util.PrintObjects(format, fns.Items, headers, row, wideExtra, wideRow)
```

Add an `AgeOf` helper to `util/output.go` (TDD it as a tiny step or fold into Task 1) :

```go
// AgeOf renders an object's age from its creation timestamp, kubectl-style.
func AgeOf(t metav1.Time) string {
	if t.IsZero() {
		return NoneValue
	}
	return duration.HumanDuration(time.Since(t.Time))
}
```

- [ ] **Step 5: Run the test to verify it passes; build + vet**

Run: `go test ./pkg/fission-cli/cmd/function/ -run TestFunctionListJSON -v && go build ./pkg/fission-cli/...`
Expected: PASS + build clean.

- [ ] **Step 6: Commit**

```bash
gofmt -w pkg/fission-cli/cmd/function/list.go pkg/fission-cli/util/output.go
git add pkg/fission-cli/cmd/function/ pkg/fission-cli/util/output.go
git commit -m "fission-cli: -o json|yaml|wide for function list"
```

---

### Task 5: Wire the remaining list commands

Apply the **exact same pattern as Task 4** to each command below. For each: (a) add `flag.Output` to the list subcommand in its `command.go`; (b) in `list.go`, parse the format, keep the existing `headers`/`row`, add `wideExtra := []string{"AGE"}` + a `wideRow` returning `util.AgeOf(item.CreationTimestamp)`, and replace `util.PrintItems(...)` with `return util.PrintObjects(format, <items>, headers, row, wideExtra, wideRow)`.

- [ ] **environment** (`cmd/environment/list.go`, `command.go`) — items `response.Items`, type `fv1.Environment`. (No READY column today; wide adds only AGE.)
- [ ] **httptrigger** (`cmd/httptrigger/list.go`, `command.go`) — note list shares `printHtSummary` in `get.go`; thread `format` into it or inline the printer in `list.go`'s run. Items `[]fv1.HTTPTrigger`.
- [ ] **timetrigger** (`cmd/timetrigger/list.go`, `command.go`) — items `tts.Items`, type `fv1.TimeTrigger`.
- [ ] **mqtrigger** (`cmd/mqtrigger/list.go`, `command.go`) — items `mqts.Items`, type `fv1.MessageQueueTrigger`.
- [ ] **kubewatch** (`cmd/kubewatch/list.go`, `command.go`) — items `ws.Items`, type `v1.KubernetesWatchTrigger` (the package aliases core/v1 as `v1`).
- [ ] **canaryconfig** (`cmd/canaryconfig/list.go`, `command.go`) — items `canaryCfgs.Items`, type `fv1.CanaryConfig`.
- [ ] **package** (`cmd/package/list.go`, `command.go`) — items are built into a filtered slice (orphan/status filters). Keep the filter loop building `[]fv1.Package`, then pass that slice to `PrintObjects`. Existing `BUILD_STATUS` column stays; wide adds AGE.

For each: write a `TestXListJSON` mirroring Task 4 (assert JSON array), run it red→green, build, commit per command (or one commit for the batch — keep them small).

- [ ] **Final step:** `go test ./pkg/fission-cli/... && go build ./...` → all pass; commit.

---

### Task 6: Wire the describe commands (json/yaml only; table/wide unchanged)

Describe commands keep their bespoke human output; they only gain json/yaml via an early return. Pattern for each:

```go
format, err := util.ParseOutputFormat(input.String(flagkey.Output))
if err != nil {
	return err
}
if handled, err := util.PrintStructured(format, obj); err != nil || handled {
	return err
}
// ... existing human rendering ...
```

- [ ] **function getmeta** (`cmd/function/getmeta.go` + `flag.Output` in `command.go`) — `obj` is the fetched `*fv1.Function`. Early-return before the `Name:`/labels printing.
- [ ] **package info** (`cmd/package/info.go` + `command.go`) — `obj` is the `*fv1.Package`; early-return before `pkgutil.PrintPackageSummary`.
- [ ] **canaryconfig get** (`cmd/canaryconfig/get.go` + `command.go`) — `obj` is `*fv1.CanaryConfig`; early-return before the table + conditions.
- [ ] **httptrigger get** (`cmd/httptrigger/get.go` + `command.go`) — `obj` is `*fv1.HTTPTrigger`; early-return before `printHtSummary` + conditions.

Note: `-o wide` on describe behaves like table (no extra columns defined) — acceptable; the value of wide on describe is marginal. Document that json/yaml are the meaningful describe formats.

- [ ] Add one describe json test (e.g. `TestGetMetaJSON`) mirroring the list test. Run red→green, build, commit.

---

### Task 7: Full verification + manual kind check

- [ ] **Step 1: Lint + unit + build**

```bash
gofmt -l pkg/fission-cli/    # expect no output
golangci-lint run ./pkg/fission-cli/...   # 0 issues
go test ./pkg/fission-cli/... ./cmd/fission-cli/...   # all pass (incl. app command-tree test, which now sees flag.Output)
go build ./...
```

- [ ] **Step 2: Manual on kind** (cluster from earlier sessions or recreate per CLAUDE.md)

```bash
go build -o /tmp/fission ./cmd/fission-cli
/tmp/fission fn list                  # unchanged table (READY column present)
/tmp/fission fn list -o wide          # adds AGE column
/tmp/fission fn list -o json | jq '.[].metadata.name'
/tmp/fission fn list -o yaml | head
/tmp/fission fn list -o bogus         # errors: invalid output format "bogus": valid values are wide, json, yaml
/tmp/fission env list -o json | jq length
/tmp/fission pkg info --name <pkg> -o yaml
/tmp/fission fn getmeta --name <fn> -o json
```

Confirm: default output byte-identical to before; `-o wide` adds AGE; json/yaml parse with jq/yq; unknown value errors clearly; describe json/yaml emits the object.

- [ ] **Step 3: Update the design doc status** to "Implemented" and commit.

- [ ] **Step 4: Push branch `cli-output-formats`** and let the user open the PR (per repo convention).

---

## Self-review notes

- **Spec coverage:** flag (Task 3), printer core (Tasks 1-2), all in-scope list commands (Tasks 4-5), all in-scope describe commands (Task 6), tests + manual (every task + Task 7). Covered.
- **Deviations from design (intentional, documented above):** JSON/YAML marshal the slice (array/sequence) rather than `---`-separated docs; wide columns appended at end (AGE last) rather than before NAMESPACE — keeps `PrintObjects` generic. `-o wide` on describe == table (no extra columns).
- **Type consistency:** `PrintObjects[T]`, `PrintStructured`, `ParseOutputFormat`, `encode`, `AgeOf` names are used identically across tasks. `flagkey.Output`/`flagkey.OutputFormat` — confirm the exact key name in Task 3 step 1 before use.
- **Backward compatibility:** default (no `-o`) path still calls `PrintItems(headers, items, row)` with the current columns → byte-identical output.
