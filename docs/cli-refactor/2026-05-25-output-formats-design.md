# Design: `-o json|yaml|wide` output formats for Fission CLI read commands

Status: Implemented (branch `cli-output-formats`).
Date: 2026-05-25.

## Context

Fission CLI `list` and describe commands print a single hard-coded human table.
There is no machine-readable output, so scripting against `fission` means parsing column text — brittle and undocumented.
`kubectl` solves this with `-o json|yaml|wide|name`; users expect the same.

The recent dedup work added shared table helpers (`util.PrintTable`, `util.PrintItems`, `util.ConditionStatus`), which gives a natural seam to introduce a format-aware printer without rewriting each command.
`sigs.k8s.io/yaml` is already a dependency (used by `spec` `SpecDry`/`crdToYaml`), so object→YAML is a solved problem.

The `-o`/`--output` flag is **already used** by *download* commands (`pkg getsrc`, `pkg getdeploy`, `archive` download, `support dump`) to mean "output file path".
Those are different commands from the read/list commands, so a format-selector `-o` on the read commands does not collide.

## Goal

Add `-o json`, `-o yaml`, and `-o wide` to the read commands.
No flag keeps today's exact table output (so #3397's default `READY` column is preserved).
Purely additive and backward compatible.

## Scope

In scope (gain `-o`):
- `list`: function, environment, httptrigger, timetrigger, mqtrigger, kubewatch (`watch`), canaryconfig, package.
- describe: `fn getmeta`, `pkg info`, `canary get`, `ht get`.

Out of scope:
- Download commands that already own `-o` (`pkg getsrc/getdeploy`, `archive`, `support dump`) — unchanged.
- `fn get` (streams raw source bytes), `pkg getsrc/getdeploy` (archive bytes) — not object views.
- `spec list` (its own multi-section format) — could follow later; not in this doc.
- `-o name` — deferred (YAGNI for now; easy to add later under the same printer).

## Design

### Flag

A new shared flag `flag.Output` (`--output`, short `-o`, type String, default `""`) registered as an Optional flag on the in-scope commands.
Empty → current table. Valid values: `json`, `yaml`, `wide`. Unknown value → error listing the valid set.

Note: a `flagkey.Output = "output"` key already exists and is aliased by `PkgOutput`/`ArchiveOutput`/`SupportOutput` for the download commands.
Introduce a distinct key (e.g. `flagkey.OutputFormat = "output"`) reused by the read commands; the download commands keep their existing flag definitions unchanged.
(Both can share the literal name `output`/`-o` because no command registers both.)

### Semantics

- `json`: marshal the (already-filtered) result. For `list` → a JSON array of the objects (`[]Function`); for describe/single → one object. Arrays chosen over a `*List` wrapper because they are simpler to consume with `jq` and the commands already work with `.Items` slices.
- `yaml`: same objects via `sigs.k8s.io/yaml`; multiple objects separated by `\n---\n`.
- `wide`: the current table plus extra columns. Minimum new column: `AGE` (from `CreationTimestamp`, rendered with `duration.HumanDuration`). Per-resource extras may be added (e.g. function `READY`-reason). Column order: existing columns, then wide-only columns, then `NAMESPACE` stays last where present.
- empty: byte-for-byte current output.

### Printer

Add to `pkg/fission-cli/util/output.go`:

```go
// OutputFormat is the validated value of -o.
type OutputFormat string
const (
    OutputTable OutputFormat = ""     // default human table
    OutputWide  OutputFormat = "wide"
    OutputJSON  OutputFormat = "json"
    OutputYAML  OutputFormat = "yaml"
)

// ParseOutputFormat validates the -o value (empty allowed).
func ParseOutputFormat(s string) (OutputFormat, error)

// PrintObjects renders items per the format. For json/yaml it marshals items;
// for table/wide it builds rows via the provided closures. headers/row are the
// default columns; wideHeaders/wideRow add the -o wide columns. A single-object
// describe view passes a one-element slice and marshals as the bare object.
func PrintObjects[T any](
    format OutputFormat,
    items []T,
    headers []string, row func(T) []string,
    wideExtra []string, wideRow func(T) []string,
) error
```

`json`/`yaml` marshal `items` directly (the CRD types already carry the right json tags).
`table` uses `headers`+`row`; `wide` uses `headers+wideExtra` and `row`+`wideRow` concatenated.
List commands call `PrintObjects(fmt, fns.Items, …)`; describe commands call a single-object variant (`PrintObject`) that marshals the bare object for json/yaml and prints the existing summary for table/wide.

### Files

- `pkg/fission-cli/util/output.go` — printer + format parsing (+ tests).
- `pkg/fission-cli/flag/flag.go`, `flag/key/key.go` — the `--output` flag for read commands.
- `pkg/fission-cli/cmd/*/command.go` — add `flag.Output` to the in-scope list/describe subcommands.
- `pkg/fission-cli/cmd/*/list.go`, `getmeta.go`, `package/info.go`, `canaryconfig/get.go`, `httptrigger/get.go` — read `-o`, define wide columns, call the printer.

## Backward compatibility

- Default output (no `-o`) is unchanged, including #3397's `READY` column and the conditions block in describe.
- No flag/command renames; `--output` is new and optional.
- Download commands' `-o` semantics are untouched.
- No CRD/flag-doc regeneration needed beyond the new flag appearing in `--help`.

## Testing

- Unit (table-driven, in `util`): `ParseOutputFormat` valid/invalid; `PrintObjects` for table/wide/json/yaml on a sample type (assert JSON parses back, YAML has `---`, wide has the extra header).
- Per-command: a representative `fn list -o json|yaml|wide` test via the dummy driver + fake clientset asserting shape.
- Manual (kind): `fission fn list -o json | jq '.[].metadata.name'`, `-o yaml`, `-o wide` shows `AGE`; unknown `-o foo` errors.

## Out of scope / future

- `-o name`, `-o jsonpath=…`, `-o custom-columns=…`.
- `spec list -o json`.
- `-o json` `*List` wrapper (kubectl-style) — revisit if users want kind/apiVersion envelope.
