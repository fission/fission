# go.mod conventions

Follow these when adding, upgrading, or removing Go dependencies in this repo.
They keep `go.mod` readable — a glance at the first block tells you everything the project *directly* depends on, separate from the transitive graph.

## The layout

`go.mod` must hold its requirements in exactly two `require (...)` blocks:

1. A **direct** block first — every dependency the module's own code (including `_test.go` files and tools) imports directly.
   No `// indirect` comments appear here.
2. An **indirect** block second — every transitively-pulled dependency, each carrying the `// indirect` comment `go mod tidy` adds.

Additional rules:

- **No single-line `require` directives** (`require foo v1.2.3`).
  `go get` sometimes appends one; fold it into the matching block.
- **One block of each kind.**
  Don't split direct or indirect requirements across multiple blocks.
- The `tool (...)`, `replace (...)`, `exclude (...)`, and `retract` directives are unaffected — this convention is only about the `require` blocks.

## Why a guard is still needed

Since Go 1.27 (this module's `go` directive), `go mod tidy` maintains the two-block layout itself:
it keeps the `// indirect` annotations correct **and** relocates a direct dependency that landed in the indirect block (or vice versa) into the right block.
The guard remains for edits that skip tidy — a hand-edited `go.mod`, or a `go get` without a follow-up tidy — and as the CI backstop.
One formatter quirk to know: a require block reduced to a **single entry** is collapsed to a single-line `require` directive, which the guard rejects; at this module's block sizes that never happens, but if it ever does, fold the line back into a block.

## Adding or changing a dependency

1. `go get <module>@<version>` (or start importing it), then `go mod tidy` — tidy places the entry in the correct block.
2. `go mod edit -fmt` to re-sort each block, then `go mod tidy` again to confirm nothing else changed.
3. `make verify-gomod` to check the layout.

## Checking

- `make verify-gomod` — fails if any `require` block mixes direct and indirect entries, if there is more than one block of either kind, or if a single-line `require` exists.
- Runs in CI as part of the lint job's "Verify dependencies" step, and locally as a prerequisite of `make code-checks` (and therefore `make check`).
- The check itself lives in [`hack/verify-gomod.sh`](../../hack/verify-gomod.sh).
