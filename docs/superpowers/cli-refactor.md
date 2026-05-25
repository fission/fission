# Fission CLI code-quality refactor — progress tracker

Backward-compatible internal cleanup of the Fission CLI (`pkg/fission-cli/`).
**No user-facing behavior changes** — same commands, flags, aliases, output columns, and spec-file format.
Goal: less duplication, clearer structure, Go-1.26 generics where they genuinely fit, plus a few real bug fixes.

Branch: `cli-refactor-dedup` (off `main` after PR #3397 merged).

## Working principles

- Readability is a hard constraint — refactored code must read *clearer*, not just shorter. Don't over-abstract generics past the point call sites stop reading like Fission code.
- Test coverage must go up. Add table-driven unit tests; use the `dummy` CLI driver + fake clientset for command-level tests.
- Repo idioms: small focused commits, lint+tests per commit, `goimports` local prefix `github.com/fission/fission`, prefer `new(value)` over `ptr.To`.

## Test-coverage baseline (`go test -cover ./pkg/fission-cli/...`)

| Package | Before | After |
|---------|--------|-------|
| cmd/spec | 0.0% | — |
| cmd | 0.0% | — |
| cmd/function | 10.2% | — |
| cmd/httptrigger | 16.6% | — |
| cmd/package/util | 11.7% | — |
| util | 5.3% | — |
| (most others) | 0.0% | — |

## Checklist

### Part 1 — `spec` subsystem generics (the big win) — DONE
- [x] Add generic reconcile core `cmd/spec/resourcetype.go` (`Object[T]` constraint, `resourceOps[T,PT]` descriptor, `applyResourceType`)
- [x] Replace 7 `applyX` funcs (`apply.go`) with descriptors; Package equality + build-wait live in its closures, HTTPTrigger dup-check in its create/update closures
- [x] Collapse `getAppliedX` → generic `filterByDeployID[T]`
- [x] Reduce `ParseYaml` switch via generic `parseResource[T]` (+ `applyCommitLabel`/`trackSourceMap` now take `metav1.Object`)
- [x] Replace 7 `destroyX` with generic `destroyResources[T]`
- [x] Tests: `spec/resourcetype_test.go` (create/update/no-op/prune/ownership/uid-stamp), `spec/spec_test.go` (ParseYaml per-kind, unknown, duplicate, commit-label) — spec coverage 0% → 7.0%
- [~] `getAllX`: already shared (defined in `list.go`, used by `validate.go`) — no cross-file dup, left as-is
- [~] `ShowX` → PrintTable: intentionally skipped to preserve exact `spec list` output (per-kind title/spacing quirks); `ExistsInSpecs`/`SpecExists` left (low value)
- [~] `ApplySubCommand.run()` watch loop split: deferred (low value, behavior-sensitive)

### Part 2 — CRUD command dedup + bug fixes
- [ ] Fix copy-paste wrong error strings (`"error in deleting function"` etc.) across create/update/delete
- [ ] Bug fix: `httptrigger/delete.go` not-found check uses shadowed `err` instead of `errs`
- [ ] `cmd.PrintItems[T]` helper; migrate list-command row loops
- [ ] `cmd.DeleteResource` helper for simple deletes
- [ ] Normalize `do/complete/run` outliers (light touch only)
- [ ] Tests for new helpers (dummy driver + fake clientset)

### Part 3 — cobra / cliwrapper / flag cleanup
- [ ] Remove dead code: `Global*` methods, `WrapperChain`, `Parse`
- [ ] Add `SubCommand(...)` factory; migrate 15 `command.go` files
- [ ] Tests for the factory / wrapper

### Verification
- [ ] `go build ./...`, `golangci-lint run ./pkg/fission-cli/...`, `go test ./pkg/fission-cli/...`
- [ ] Coverage delta recorded above
- [ ] kind e2e: spec init→validate→apply→idempotent re-apply→--delete→destroy
- [ ] grep confirms no stray `"error in deleting function"`
