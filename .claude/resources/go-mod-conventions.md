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

## Why a guard is needed

`go mod tidy` keeps the `// indirect` annotations correct, but it does **not** move entries between blocks.
A dependency added by `go get` (or promoted from indirect to direct when you start importing it) stays in whatever block it landed in, so a direct dependency silently accumulates inside the indirect block over time.
The guard catches that drift; `go mod tidy` alone will not.

## Adding or changing a dependency

1. `go get <module>@<version>` (or start importing it), then `go mod tidy`.
2. If the new or newly-direct dependency landed in the indirect block, move its line into the direct block.
3. `go mod edit -fmt` to re-sort each block, then `go mod tidy` again to confirm nothing else changed.
4. `make verify-gomod` to check the layout.

## Checking

- `make verify-gomod` — fails if any `require` block mixes direct and indirect entries, if there is more than one block of either kind, or if a single-line `require` exists.
- Runs in CI as part of the lint job's "Verify dependencies" step, and locally as a prerequisite of `make code-checks` (and therefore `make check`).
- The check itself lives in [`hack/verify-gomod.sh`](../../hack/verify-gomod.sh).
