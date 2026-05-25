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
| cmd/spec | 0.0% | 7.0% |
| cmd/function | 10.2% | 10.3% |
| cmd/httptrigger | 16.6% | 16.9% |
| cmd/package/util | 11.7% | 11.7% |
| util | 5.3% | 7.5% |
| cmd/fission-cli/app | 0.0% | 68.2% |

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

### Part 2 — CRUD command dedup + bug fixes — DONE
- [x] Fix copy-paste wrong error strings: `"error in deleting function"` → operation-correct in httptrigger/timetrigger/mqtrigger create+update+delete; `"error creating environment"` → correct op in environment delete/update/get/pods; `AggregateValidationErrors("Environment")` → `"Package"` in package commands (function delete left — message was already correct)
- [x] Bug fix: `httptrigger/delete.go` now ignores not-found *per delete* (was checking shadowed `err`, and `IsNotFound` can't unwrap an `errors.Join`)
- [x] `util.PrintItems[T]` helper (sugar over PrintTable); migrated function/environment/timetrigger/mqtrigger/kubewatch/canaryconfig list row loops + test
- [~] `cmd.DeleteResource`: skipped — only `function` delete is truly simple; others have dependency checks, a helper would obscure them
- [~] `do/complete/run` normalization: skipped — low value, behavior-sensitive

### Part 3 — cobra / cliwrapper / flag cleanup — DONE
- [x] Remove dead code: 7 `Global*` methods (from `cli.Input` + cobra + dummy drivers), `WrapperChain`, `Parse` (all verified unused repo-wide)
- [x] Add `wrapper.SubCommand(c, action, flags)` factory (preserves all cobra.Command fields); migrated all 15 `command.go` files (71 subcommands) — removes the per-subcommand `RunE: Wrapper(...)` + separate `SetFlags(...)` pair
- [x] Tests: `cmd/fission-cli/app/app_test.go` walks the whole command tree asserting every leaf has RunE wired + key command paths present (app coverage 0% → 68.2%)

### Verification
- [ ] `go build ./...`, `golangci-lint run ./pkg/fission-cli/...`, `go test ./pkg/fission-cli/...`
- [ ] Coverage delta recorded above
- [ ] kind e2e: spec init→validate→apply→idempotent re-apply→--delete→destroy
- [ ] grep confirms no stray `"error in deleting function"`
