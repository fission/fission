# Fission Security Scan Remediation Plan — Round 2 (2026-05)

> **For agentic workers:** REQUIRED SUB-SKILL — use `superpowers:executing-plans` to work this plan task-by-task.
> Steps use `- [ ]` checkboxes for tracking.
> Each batch = one (or a small set of) commit(s) on a single long-lived branch; do not bundle batches.

**Goal:** Triage and fix the 218 findings in the new `output.sarif` (Harness SAST + SCA, scanned 2026-05-07) using a batched, resumable workflow.
This is round 2 — the previous round (PR #3361) closed `directory-traversal-http`, `oss_vuln`, and reduced `loose-file-permissions`, `file-toctou`, and `timing-password-compare`.
Round 2 focuses on **mechanical fixes** to the remaining surface in Go-related findings across server, CLI, and Helm/charts; no architectural work, no RFC implementation, no re-litigation of previously-accepted-risk classifications.

**Architecture:**
All plan + progress + canonical index live under `.security-fixes/` at the repo root; the index `.security-fixes/findings-index.md` is the single source of truth and groups findings by rule + package + batch with status.
All work happens on a single long-lived branch `security-fixes-2026-05` — each batch is one (or a small set of) commit(s) on that branch.
Per-PR scrub of `.security-fixes/` workspace before the PR opens (matches round-1 convention; reviewers see only source-code changes).

**Tech stack:** Go 1.26, controller-runtime, Helm chart `charts/fission-all`, `golangci-lint` for static checks, `setup-envtest` (k8s 1.30.x) for `make test-run`.

---

## Context

The new SARIF reports 218 findings (down from 251 last round).
Confirmed closures from PR #3361:
- `directory-traversal-http` (was 4) — closed by `Builder.Clean` `SanitizeFilePath`.
- `oss_vuln` CVE-2026-26958 (was 1) — closed by `filippo.io/edwards25519` bump.
- `loose-file-permissions` 11 → 6 — partial close.
- `file-toctou` 76 → 72 — partial close.
- `timing-password-compare` 11 → 10 — auth.go closed.

What's still flagged:

| CVSS | Rule | Count | Round-2 verdict |
|---|---|---|---|
| 9 | `command-injection-attacker-controlled` | 4 | Scanner re-flag of B1 allowlisted exec; **document, no code change** |
| 8.5 | `warning-sink-execute` | 6 | 2 builder dup of above + 4 in `pkg/plugin` (out of scope per user) |
| 8 | `warning-sink-html` | 10 | FP — JSON content-type handlers; document |
| 5.5 | `Secret Keyword` (per-file rule IDs) | ~50 | FP — flag/const/struct-field names; document at rule level |
| 5 | `idor-cloud-access` / `idor-collection-access` / `idor-pattern-based` / `idor-external-api` | 10 | Architectural; NetworkPolicy mitigation in tree; RFC 0001 deferred. **Document.** |
| 5 | `go-tls-skip-verify` | 1 | `test/e2e/framework/framework.go:144` — test code, **out of scope** |
| 4.5 | `sleep-from-attacker-controlled` | 18 | FP — constant `time.Sleep` in CLI poll loops; document |
| 4 | `idor-collection-access` | 2 | rolled into idor cluster |
| 3 | `timing-password-compare` | 10 | **All FP** — RV / namespace / container-config diff for state reconcile; document per-site |
| 3 | `file-toctou` | 72 | **TP-mechanical**; refactor open-then-stat across batches B1–B3 |
| 2.5 | `log-forging-attacker` (2) + `http-to-log` (2) | 4 | 2 FP (console/log.go:55 already sanitised) + 2 TP (`pkg/builder/builder.go:298,311`); fix B2 |
| 2 | `time-file` | 4 | FP — filename suffixes only; document |
| 1 | `loose-file-permissions` | 6 | 4 in-scope TPs (`spec/init.go` × 3, `logger.go:163`); fix B4 |
| 1 | `http-no-ssl` | 1 | Accepted-risk — in-cluster TLS terminated by ingress; document |

**Working layout (created by B0):**

```
.security-fixes/
├── README.md             # entrypoint
├── plan.md               # copy of this plan
├── findings-index.md     # canonical per-rule + per-finding index (round-2)
├── progress.md           # ledger of merged work
└── sarif-summary.sh      # query helper (jq wrapper)
```

**Working branch:** `security-fixes-2026-05` (created off `main` at the start of B0).
All batch commits land on this branch in order.

**Scope (locked in brainstorm):** Server Go + CLI + Helm/charts.
**Out of scope:** `pkg/plugin/*`, `test/*`, `test/integration/testdata/*`, the deferred RFC 0001 HMAC implementation, scanner re-runs.

---

## Strategy

1. **Triage is canonical, not advisory.** Every rule goes into `.security-fixes/findings-index.md` with TP/FP/accepted/architectural status and one-sentence reasoning.
2. **Batch by package, not by rule.** Same idiom as round 1.
3. **Each batch must compile and pass tests in isolation.** Verification recipe is fixed at the end of every batch.
4. **No suppressions in source.** No `//nolint`, no `.harness/ignore.yaml`, no `.checkov.yaml`. The index is authoritative.
5. **Resumability.** A worker reads `.security-fixes/progress.md` to find the next pending batch.
6. **Pre-PR scrub.** `.security-fixes/` is committed during work and removed in a final scrub commit before opening the PR — same as round 1.

---

## Batches (execution order on the single `security-fixes-2026-05` branch)

| Batch | Package(s) / theme | Findings closed | Effort |
|---|---|---|---|
| **B0** | `.security-fixes/` workspace + branch + fresh index | none (triage)        | S |
| **B1** | `pkg/utils/utils.go`, `pkg/utils/zip.go` (shared TOCTOU helpers) | up to 36 file-toctou notes | M |
| **B2** | `pkg/fetcher/fetcher.go` TOCTOU + `pkg/builder/builder.go` log-forging | up to 10 toctou + 2 log-forging | M |
| **B3** | Misc TOCTOU sweep: `cmd/*`, `pkg/executor/util/util.go`, `pkg/featureconfig/config.go`, `pkg/logger/logger.go`, `pkg/fission-cli/cmd/{spec,support,package}/*` | up to 26 file-toctou notes | M |
| **B4** | `pkg/fission-cli/cmd/spec/init.go` + `pkg/logger/logger.go` file-mode tightening | 4 loose-file-permissions notes | S |
| **B5** | Index finalisation + accepted-risk register; pre-PR scrub of workspace | n/a | S |

**Why not run B1-B3 in parallel?** They share `pkg/utils` helpers — B1 lands the helper changes; B2 and B3 may depend on them. Sequencing keeps each batch's diff minimal and reviewable.

---

## Batch detail

### B0 — Branch + `.security-fixes/` workspace

**Files:**
- Create: `.security-fixes/README.md`
- Create: `.security-fixes/plan.md` (copy of this plan)
- Create: `.security-fixes/findings-index.md`
- Create: `.security-fixes/progress.md`
- Create: `.security-fixes/sarif-summary.sh`
- Verify: `.gitignore` already ignores `output.sarif` (round-1 already added it)

- [ ] **Step 1: Create the working branch.**

  Run from repo root:

  ```bash
  git fetch origin
  git checkout main
  git pull --ff-only origin main
  git checkout -b security-fixes-2026-05
  ```

  Expected: branch `security-fixes-2026-05` exists, `git status` clean except for untracked `.security-fixes/`, `output.sarif`, `oci/`, `rfc/`.

- [ ] **Step 2: Confirm `output.sarif` is ignored.**

  Run: `git check-ignore -v output.sarif`.
  Expected: matches a rule in `.gitignore`. If not, add `output.sarif` to `.gitignore` and commit (combine with Step 9's commit).

- [ ] **Step 3: Author `.security-fixes/sarif-summary.sh`.**

  ```bash
  #!/usr/bin/env bash
  # Summarise a Harness SAST SARIF file. Usage: .security-fixes/sarif-summary.sh [path]
  set -euo pipefail
  SARIF="${1:-output.sarif}"
  [[ -f "$SARIF" ]] || { echo "no SARIF at $SARIF" >&2; exit 2; }

  echo "== severity =="
  jq -r '.runs[].results[] | .level // "?"' "$SARIF" | sort | uniq -c

  echo
  echo "== findings by rule (CVSS desc) =="
  jq -r '
    (.runs[].tool.driver.rules // []) as $rules
    | (.runs[].results // []) as $res
    | $rules | map({key: .id, value: (.properties["security-severity"] // "0")}) | from_entries as $sev
    | $res
    | group_by(.ruleId)
    | map({rule: .[0].ruleId, count: length, cvss: ($sev[.[0].ruleId] // "?")})
    | sort_by(.cvss | tonumber? // 0) | reverse
    | .[] | "\(.cvss)\t\(.count)\t\(.rule)"
  ' "$SARIF"

  echo
  echo "== top 20 packages =="
  jq -r '.runs[].results[].locations[0].physicalLocation.artifactLocation.uri // "?"' "$SARIF" \
    | awk -F/ 'NF>=2{print $1"/"$2} NF==1{print $1}' | sort | uniq -c | sort -rn | head -20

  echo
  echo "== distinct sites for a rule (pass rule id as arg 2) =="
  if [[ -n "${2:-}" ]]; then
    jq -r --arg r "$2" '.runs[].results[] | select(.ruleId==$r)
      | "\(.locations[0].physicalLocation.artifactLocation.uri):\(.locations[0].physicalLocation.region.startLine)"' "$SARIF" \
      | sort -u
  fi
  ```

  Run: `chmod +x .security-fixes/sarif-summary.sh && .security-fixes/sarif-summary.sh output.sarif`.
  Expected: prints severity table (`error 20 / warning 94 / note 104`), CVSS-ordered rule table, per-package histogram.

- [ ] **Step 4: Author `.security-fixes/findings-index.md`.**

  Required structure (mirrors round 1, refreshed for round 2):

  1. **Header** — scan tool `Harness SAST and SCA 1.0.0`, scan date `2026-05-07`, source `output.sarif` (gitignored), totals (218 findings: 20 errors / 94 warnings / 104 notes).
  2. **Round-2 deltas vs round-1** — table of rule-count changes (closures + reductions; see Context section above).
  3. **Quick query examples** — copy-pasteable `jq` snippets and `.security-fixes/sarif-summary.sh <sarif> <ruleId>` invocations.
  4. **Per-rule index** — one row per rule with: rule id, CVSS, count, verdict (`TP-mechanical` / `FP` / `accepted-risk` / `architectural-deferred`), batch (`B1`-`B5` or `—`), reasoning. Seed from the table in Context.
  5. **Per-finding rows for true positives only** — file:line, function, fixing batch, status (`open` initially; flipped to `fixed-<sha>` per batch).
  6. **Accepted-risk register** — one paragraph per category with rationale and the boundary that justifies it:
     - `command-injection-attacker-controlled` (4) — B1 allowlist holds; scanner can't see `resolveBuildCommand` validation. Reference: `pkg/builder/builder.go` `resolveBuildCommand` + `defaultBuildCommand` allowlist.
     - `warning-sink-execute` (6) — 4 in `pkg/plugin/*` are out-of-scope CLI plugin loader; 2 in `pkg/builder/builder.go` are dup of cmd-injection (allowlisted).
     - `warning-sink-html` (10) — every cited handler sets `Content-Type: application/json`; reference any one (e.g., `pkg/router/router.go`).
     - `idor-*` (10) — NetworkPolicy `charts/fission-all/templates/networkpolicy.yaml` mitigates inter-pod surface; long-term application-layer auth tracked by RFC 0001 (deferred from round 1).
     - `go-tls-skip-verify` (1) — only remaining site is `test/e2e/framework/framework.go:144`; test infra, not production path.
     - `sleep-from-attacker-controlled` (18) — every alias is a constant `time.Sleep` in CLI poll loops (e.g., `pkg/fission-cli/cmd/spec/buildwatch.go:116`); not attacker-controlled despite the name.
     - `timing-password-compare` (10) — every flagged compare is on `ResourceVersion` / namespace / container-config fields used by reconcile loops to decide whether to roll out; not credential comparison. Per-site rationale lives in this register.
     - `time-file` (4) — `time.Now().UnixNano()` used as filename suffix in `pkg/fission-cli/cmd/support/dump.go`; not a security boundary.
     - `http-no-ssl` (1) — internal cluster service; TLS terminated by ingress.
     - `Secret Keyword` (~50) — flag/const/struct-field names matching keyword heuristics; reviewed once at rule level, not per-finding.
  7. **Re-scan workflow** — three steps to consume a future SARIF: regenerate via `.security-fixes/sarif-summary.sh output.sarif`, diff CVSS-ordered rule counts vs the per-rule index in this file; any new rule or count delta is real signal.

- [ ] **Step 5: Author `.security-fixes/progress.md`.**

  ```markdown
  # Security Fixes Round 2 — Progress Ledger

  Branch: `security-fixes-2026-05`. See `plan.md` for batch detail and `findings-index.md` for triage.

  | Batch | Status   | Commit | Notes |
  |-------|----------|--------|-------|
  | B0    | done     | <sha>  | branch + workspace + fresh index |
  | B1    | pending  |        | pkg/utils TOCTOU sweep (utils.go, zip.go) |
  | B2    | pending  |        | pkg/fetcher TOCTOU + pkg/builder log-forging |
  | B3    | pending  |        | misc TOCTOU sweep (cmd/*, executor, featureconfig, logger, fission-cli) |
  | B4    | pending  |        | file mode tightening (spec/init.go, logger.go) |
  | B5    | pending  |        | index finalisation + pre-PR scrub |
  ```

- [ ] **Step 6: Author `.security-fixes/README.md`.**

  ```markdown
  # Security Fixes Workspace — Round 2

  This folder holds the working artefacts for the 2026-05-07 SAST remediation.

  - `plan.md` — full plan with per-batch tasks. Start here.
  - `findings-index.md` — canonical triage of every finding in `output.sarif`.
  - `progress.md` — batch ledger. Read this to find what's next.
  - `sarif-summary.sh` — query helper.

  Source SARIF (`output.sarif`) is gitignored. Re-run the scanner to refresh it.
  This folder is scrubbed in the final batch before the PR opens — reviewers see only source-code changes.
  ```

- [ ] **Step 7: Copy this plan in.**

  ```bash
  cp /Users/sanketsudake/.claude-personal/plans/2026-05-07-fission-security-fixes-2026-05.md .security-fixes/plan.md
  ```

- [ ] **Step 8: Run `make code-checks`.**

  Run: `make code-checks`.
  Expected: PASS (this batch only adds docs and a shell script).

- [ ] **Step 9: Commit.**

  ```bash
  git add .security-fixes/ .gitignore
  git commit -m "Bootstrap security-fixes-2026-05 workspace and round-2 index (B0)"
  ```

  Update `.security-fixes/progress.md` row B0 with the resulting SHA in a follow-up tiny commit, OR amend before pushing (acceptable since nothing has been pushed yet on this branch).

---

### B1 — `pkg/utils` TOCTOU sweep (shared helpers)

**Files:**
- Modify: `pkg/utils/utils.go` (sites: 78, 82, 120, 128, 193, 210, 284, 288)
- Modify: `pkg/utils/zip.go` (sites: 14, 45, 56, 62, 76, 86, 94, 106)
- Test: `pkg/utils/utils_test.go`, `pkg/utils/zip_test.go` (extend, not rewrite)
- Update: `.security-fixes/findings-index.md` and `.security-fixes/progress.md`

**Site classification (read source first, then decide):**

| Site | Pattern | Verdict | Action |
|---|---|---|---|
| `utils.go:78` (`filepath.Abs(p)` in `FindAllGlobs`) | not file open | scanner FP | document |
| `utils.go:82` (`filepath.Glob(path)`) | not file open | scanner FP | document |
| `utils.go:120` (`os.Stat(filePath)` in `FileSize`) | Stat-only, no follow-up Open in same fn | scanner FP | document |
| `utils.go:128` (`os.Open(fileName)` in `GetFileChecksum`) | Open-then-Read; no Stat-before-Open | scanner FP | document |
| `utils.go:193` (`os.Create(localPath)` in `DownloadUrl`) | Create + later Chmod 0600 = TOCTOU window | **TP** | refactor |
| `utils.go:210` (`os.Chmod(localPath, 0600)`) | dup of 193 | rolled into 193 fix | rolled in |
| `utils.go:284` (`os.RemoveAll(pkgPath)` in `DeleteOldPackages`) | RemoveAll, no race meaningful (check sharedVolumePath HasPrefix already) | scanner FP | document |
| `utils.go:288` (`os.RemoveAll(file)`) | dup of 284 | rolled in | document |
| `zip.go:14` (`os.Open(filename)` in `IsZip`) | Open-then-Match | scanner FP | document |
| `zip.go:45` (`os.Create(targetName)` in `MakeZipArchiveWithGlobs`) | Create + close on defer | **TP** | replace with `os.OpenFile` `O_CREATE\|O_EXCL\|O_WRONLY 0o600` |
| `zip.go:56` (`filepath.Abs(targetName)`) | not file op | scanner FP | document |
| `zip.go:62` (`os.Stat(src)` in `Archive`) | Stat→use info.IsDir, no follow-up Open of `src` in same fn | scanner FP | document |
| `zip.go:76` (`os.Open(src)` in `Unarchive`) | Open-then-Extract | scanner FP | document |
| `zip.go:86` (`os.MkdirAll(destPath, f.Mode())`) | iteration over zip entries; mode comes from archive | low-priority TP | leave (changing mode flow risks breaking shared-volume access — see CLAUDE.md "things that bite") |
| `zip.go:94` (`os.MkdirAll(filepath.Dir(destPath), 0o755)`) | known good — round 1 fixed shared-volume permissions | document | document |
| `zip.go:106` (`os.Create(destPath)` in `Unarchive`) | Create + later Chmod | **TP** | replace with `os.OpenFile` `O_CREATE\|O_EXCL\|O_WRONLY` and remove the separate Chmod call |

So the actual mechanical fixes in B1 are:
1. `utils.go:193-213` (DownloadUrl) — fold mode into OpenFile
2. `zip.go:45-49` (MakeZipArchiveWithGlobs) — fold mode into OpenFile
3. `zip.go:106-114` (Unarchive) — fold archive entry mode into OpenFile

The other sites are scanner-FP and will be documented in the index.

**TDD note for B1:** these are mechanical refactors that materialise the file mode at `OpenFile` time instead of via a follow-up `Chmod` call. Observable behaviour (final file mode, overwrite semantics) is unchanged — the only difference is closing a sub-millisecond window where the file briefly carried a wider mode. We don't add `O_EXCL` (would change overwrite semantics for callers that intentionally re-download). A regression-guard test asserts the final mode; existing tests assert callers still work end-to-end.

- [ ] **Step 1: Audit callers of `DownloadUrl` to confirm overwrite semantics expected.**

  ```bash
  grep -rn "DownloadUrl(" --include='*.go' pkg/ cmd/ | grep -v _test.go
  ```

  Expected: callers in `pkg/fission-cli/cmd/package/util/util.go`, `pkg/fetcher/fetcher.go`. Read each call site to confirm overwrite-on-second-call is acceptable (it should be; package-cache refresh is a normal flow).
  Record the audit result in the index entry for `utils.go:193`.

- [ ] **Step 2: Refactor `DownloadUrl`.**

  In `pkg/utils/utils.go`, replace the block currently at lines 193-213:

  ```go
  // before
  w, err := os.Create(localPath)
  if err != nil {
      return err
  }
  defer w.Close()

  _, err = io.Copy(w, resp.Body)
  if err != nil {
      return err
  }

  // flushing write buffer to file
  err = w.Sync()
  if err != nil {
      return err
  }

  err = os.Chmod(localPath, 0600)
  if err != nil {
      return err
  }

  return nil
  ```

  With:

  ```go
  // after — atomic mode-at-create; preserves os.Create's overwrite semantics
  // (O_RDWR|O_CREATE|O_TRUNC) but with explicit 0o600 mode.
  w, err := os.OpenFile(localPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
  if err != nil {
      return err
  }
  defer w.Close()

  if _, err = io.Copy(w, resp.Body); err != nil {
      return err
  }

  // flushing write buffer to file
  if err = w.Sync(); err != nil {
      return err
  }

  return nil
  ```

  The separate `os.Chmod(localPath, 0600)` call is gone — its job is done at `OpenFile` time.

- [ ] **Step 3: Add regression-guard test.**

  Append to `pkg/utils/utils_test.go` (create the file if absent):

  ```go
  package utils

  import (
      "context"
      "net/http"
      "net/http/httptest"
      "os"
      "path/filepath"
      "testing"
  )

  func TestDownloadUrl_FileModeIs0600(t *testing.T) {
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          _, _ = w.Write([]byte("payload"))
      }))
      t.Cleanup(srv.Close)

      dir := t.TempDir()
      dst := filepath.Join(dir, "out.bin")
      if err := DownloadUrl(context.Background(), srv.Client(), srv.URL, dst); err != nil {
          t.Fatalf("DownloadUrl: %v", err)
      }
      fi, err := os.Stat(dst)
      if err != nil {
          t.Fatalf("Stat: %v", err)
      }
      if got := fi.Mode().Perm(); got != 0o600 {
          t.Fatalf("mode: got %#o, want 0600", got)
      }
  }

  func TestDownloadUrl_OverwriteAllowed(t *testing.T) {
      // Ensures the refactor preserved os.Create's overwrite semantics —
      // re-downloading to the same path must succeed.
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          _, _ = w.Write([]byte("v2"))
      }))
      t.Cleanup(srv.Close)

      dir := t.TempDir()
      dst := filepath.Join(dir, "out.bin")
      if err := os.WriteFile(dst, []byte("v1"), 0o644); err != nil {
          t.Fatal(err)
      }
      if err := DownloadUrl(context.Background(), srv.Client(), srv.URL, dst); err != nil {
          t.Fatalf("DownloadUrl on existing path: %v", err)
      }
      got, err := os.ReadFile(dst)
      if err != nil || string(got) != "v2" {
          t.Fatalf("expected overwrite to v2, got %q (err=%v)", got, err)
      }
  }
  ```

- [ ] **Step 4: Run the new tests.**

  Run: `go test -race -count=1 -run 'TestDownloadUrl_' ./pkg/utils/...`.
  Expected: PASS — both tests now and after refactor (regression guards).

- [ ] **Step 5: Refactor `MakeZipArchiveWithGlobs`.**

  In `pkg/utils/zip.go`, replace `out, err := os.Create(targetName)` (currently line 45) with:

  ```go
  // OpenFile with explicit 0o600 mode — same overwrite semantics as os.Create,
  // but the mode is materialised at create time (no Chmod-after-Create window).
  out, err := os.OpenFile(targetName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
  if err != nil {
      return "", fmt.Errorf("failed to create archive file: %w", err)
  }
  ```

  No other change in this function. Existing `defer out.Close()` and downstream code is unchanged.

- [ ] **Step 6: Refactor `Unarchive` extract-loop.**

  In `pkg/utils/zip.go`, replace the destFile creation block (currently lines 106-114) with:

  ```go
  // Materialise the archive entry's mode at create time so a concurrent
  // observer never sees the file at a wider mode. Same overwrite semantics
  // as os.Create.
  destFile, err := os.OpenFile(destPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, f.Mode().Perm())
  if err != nil {
      return fmt.Errorf("failed to create file in destination: %w", err)
  }
  defer destFile.Close()
  ```

  Delete the `err = destFile.Chmod(f.Mode())` call and its error block (was lines 111-114) — its job is done at `OpenFile` time.

  **Note:** `f.Mode().Perm()` strips type bits, leaving only permission bits. The umask is `os.OpenFile`-default (typically 0022) — same as `os.Create`'s 0666 path, so behaviour matches Round 1's intent for shared-volume access.

- [ ] **Step 7: Run the full `pkg/utils` test suite + lint.**

  Run: `go test -race -count=1 ./pkg/utils/... && golangci-lint run ./pkg/utils/...`.
  Expected: PASS.

- [ ] **Step 8: Run `make test-run`.**

  `Unarchive` is called by `pkg/fetcher` and `pkg/storagesvc`; broader tests verify the path still works end-to-end.
  Run: `make test-run`.
  Expected: PASS.

- [ ] **Step 9: Update findings index + progress ledger.**

  In `.security-fixes/findings-index.md`, mark the 3 mechanical TP rows (`utils.go:193`, `zip.go:45`, `zip.go:106`) as `fixed-<placeholder>` (rewrite with real SHA after commit if tree not yet pushed).
  Mark the 13 remaining `pkg/utils/*` toctou rows as `accepted-fp-<reasoning>` with the reasoning from the site classification table above (one row per distinct line).
  Flip B1 in `.security-fixes/progress.md` to `done`.

- [ ] **Step 10: Commit.**

  ```bash
  git add pkg/utils/utils.go pkg/utils/zip.go pkg/utils/utils_test.go .security-fixes/
  git commit -m "Atomic file-mode-on-create in pkg/utils download and zip helpers (B1)"
  ```

---

### B2 — `pkg/fetcher` TOCTOU + `pkg/builder` log forging

**Files:**
- Modify: `pkg/fetcher/fetcher.go` (sites: 74, 262, 315, 549, 603)
- Modify: `pkg/builder/builder.go` (line 298 — log-forging-attacker)
- Test: `pkg/fetcher/fetcher_test.go`, `pkg/builder/builder_test.go`
- Update: `.security-fixes/findings-index.md` and `.security-fixes/progress.md`

**Site classification:**

| Site | Pattern | Verdict | Action |
|---|---|---|---|
| `fetcher.go:74` (`os.MkdirAll(dirPath, 0o750)` in `makeVolumeDir`) | mode 0750 on the shared volume root | possible-bite per CLAUDE.md (round-1 found 0o750 broke cross-container access on `/packages`) — but `makeVolumeDir` initialises three roots, *the directories themselves* are not re-used for cross-UID file access; entries written under them are. **Document, no change.** | document |
| `fetcher.go:262` (read inside `Fetch`) | open-then-read pattern, common | scanner FP | document |
| `fetcher.go:315` (read inside `FetchSecretsAndCfgMaps`) | same as above | scanner FP | document |
| `fetcher.go:549` (write inside `Upload`) | should be Open-then-fstat (round-1 already did this for the storagesvc side; verify fetcher) | **read source**; if Stat-before-Open, refactor; if not, document | conditional |
| `fetcher.go:603` (download path) | Open-then-Read or Stat-then-Open? | **read source**; same as above | conditional |
| `builder.go:298` (`fmt.Println(output)`) | streams build script stdout to stdout — output is fully attacker-controlled (env image) | **TP** | sanitise via `console.sanitizeLogLine` analogue or in-place CR/LF replacer |
| `builder.go:311` (`fmt.Println(cmdErr)`) | cmdErr wraps the `command` (allowlisted by B1) and `cmd.Wait` error | scanner FP after B1 allowlist | document |

The two TPs requiring code changes in this batch are `builder.go:298` and any `fetcher.go:549` / `:603` site that turns out to be Stat-before-Open.
Read those two fetcher sites carefully before refactoring.

- [ ] **Step 1: Read the cited fetcher sites and classify.**

  Run:

  ```bash
  sed -n '255,275p' pkg/fetcher/fetcher.go
  sed -n '305,330p' pkg/fetcher/fetcher.go
  sed -n '540,560p' pkg/fetcher/fetcher.go
  sed -n '595,615p' pkg/fetcher/fetcher.go
  ```

  For each: write the verdict (TP-mechanical / scanner-FP) into the per-finding index row in `.security-fixes/findings-index.md`. Stop and refactor only the TP sites in the steps below.

- [ ] **Step 2: Write the failing test for builder log sanitisation.**

  Add to `pkg/builder/builder_test.go` (file exists; add this test plus any imports below the existing `TestBuilder`):

  ```go
  func TestBuilderBuild_StripsCRLFFromBuildOutput(t *testing.T) {
      // Build script that emits a payload containing CR/LF — simulates a
      // hostile build script trying to inject fake log lines into the
      // builder's stdout.
      script := `#!/bin/sh
  printf 'real-line\nFAKE\rINJECTED\n'
  `
      dir := t.TempDir()
      buildScript := filepath.Join(dir, "build.sh")
      if err := os.WriteFile(buildScript, []byte(script), 0o755); err != nil {
          t.Fatal(err)
      }
      srcPath := filepath.Join(dir, "src")
      if err := os.MkdirAll(srcPath, 0o755); err != nil {
          t.Fatal(err)
      }
      dstPath := filepath.Join(dir, "deploy.zip")

      // Capture stdout for the duration of the build call.
      r, w, err := os.Pipe()
      if err != nil {
          t.Fatal(err)
      }
      old := os.Stdout
      os.Stdout = w
      t.Cleanup(func() { os.Stdout = old })

      b := MakeBuilder(loggerfactory.GetLogger(), dir)
      _, err = b.build(context.Background(), buildScript, nil, srcPath, dstPath)
      if err != nil {
          t.Fatalf("build: %v", err)
      }
      _ = w.Close()
      out, _ := io.ReadAll(r)

      // Embedded CR must not appear as a literal control char in captured stdout —
      // it should be escaped to "\r" (literal backslash-r).
      if bytes.Contains(out, []byte("FAKE\rINJECTED")) {
          t.Fatalf("stdout contains unsanitised CR: %q", out)
      }
      if !bytes.Contains(out, []byte(`FAKE\rINJECTED`)) {
          t.Fatalf("stdout missing escaped form: %q", out)
      }
  }
  ```

  Required imports for this test (add only those not already present in `builder_test.go`):

  ```go
  "context"
  "path/filepath"
  ```

  `bytes`, `io`, `os`, `testing`, and `loggerfactory` are already imported.

- [ ] **Step 3: Run the test; expect failure.**

  Run: `go test -race -count=1 -run TestBuilderBuild_StripsCRLFFromBuildOutput ./pkg/builder/...`.
  Expected: FAIL — current `fmt.Println(output)` emits raw bytes including `\r`.

- [ ] **Step 4: Sanitise the build output stream.**

  In `pkg/builder/builder.go`, locate the Runtime-logs scanner block (around line 295-300):

  ```go
  for scanner.Scan() {
      output := scanner.Text()
      fmt.Println(output)
      fmt.Fprintf(&buildLogs, "%s\n", output)
  }
  ```

  Replace with:

  ```go
  for scanner.Scan() {
      output := sanitizeBuildLogLine(scanner.Text())
      fmt.Println(output)
      fmt.Fprintf(&buildLogs, "%s\n", output)
  }
  ```

  And add the helper near the top of the file (under existing imports):

  ```go
  // sanitizeBuildLogLine neutralises embedded CR/LF in build-script stdout
  // before echoing it to the builder pod's stdout, so a hostile build script
  // cannot inject fake log lines (CWE-117). bufio.Scanner's default split
  // function already strips trailing newlines; this replacer handles
  // embedded CR/LF that survive a single Scan().
  var buildLogReplacer = strings.NewReplacer("\r", "\\r", "\n", "\\n")

  func sanitizeBuildLogLine(s string) string {
      if !strings.ContainsAny(s, "\r\n") {
          return s
      }
      return buildLogReplacer.Replace(s)
  }
  ```

  Add `"strings"` to the imports if not already present.

- [ ] **Step 5: Run the test; expect PASS.**

  Run: `go test -race -count=1 -run TestBuilderBuild_StripsCRLFFromBuildOutput ./pkg/builder/...`.
  Expected: PASS.

- [ ] **Step 6: Apply Open-then-Stat refactor to fetcher TP sites identified in Step 1.**

  For each TP site, replace the pattern:

  ```go
  // before
  fi, err := os.Stat(p)
  if err != nil { ... }
  f, err := os.Open(p)
  ```

  With:

  ```go
  // after
  f, err := os.Open(p)
  if err != nil { ... }
  defer f.Close()
  fi, err := f.Stat()
  ```

  (Same pattern for `os.OpenFile`. If the existing code branches on `fi.IsDir()`, use `fi.IsDir()` after `f.Stat()` — semantics unchanged.)

  If a site turns out to be FP after Step 1, leave it and document it in the index instead.

- [ ] **Step 7: Run focused tests + lint for fetcher/builder.**

  Run: `go test -race -count=1 ./pkg/fetcher/... ./pkg/builder/... && golangci-lint run ./pkg/fetcher/... ./pkg/builder/...`.
  Expected: PASS.

- [ ] **Step 8: Run `make test-run`.**

  Run: `make test-run`.
  Expected: PASS.

- [ ] **Step 9: Update findings index + progress ledger.**

  In `.security-fixes/findings-index.md`:
  - Mark `builder.go:298` log-forging row as `fixed-<sha>`.
  - Mark `builder.go:311` row as `accepted-fp-allowlisted-command-name` with one-line rationale (allowlisted by B1 `resolveBuildCommand`).
  - Mark each refactored fetcher TOCTOU site as `fixed-<sha>`.
  - Mark each fetcher FP site as `accepted-fp-<reasoning>`.
  Flip B2 in `progress.md` to `done`.

- [ ] **Step 10: Commit.**

  ```bash
  git add pkg/fetcher/fetcher.go pkg/builder/builder.go pkg/builder/builder_test.go .security-fixes/
  git commit -m "Sanitise build-script stdout; open-then-stat in fetcher (B2)"
  ```

---

### B3 — Misc TOCTOU sweep

**Files (TOCTOU sites by package, in alphabetical order):**

- `cmd/builder/main.go:44`
- `cmd/fetcher/app/server.go:60`
- `cmd/fission-bundle/mqtrigger/mqtrigger.go:107, 122`
- `pkg/executor/util/util.go:132`
- `pkg/featureconfig/config.go:39`
- `pkg/logger/logger.go:92, 163`
- `pkg/fission-cli/cmd/package/package.go:55`
- `pkg/fission-cli/cmd/package/util/util.go:121`
- `pkg/fission-cli/cmd/spec/apply.go:257`
- `pkg/fission-cli/cmd/spec/init.go:135`
- `pkg/fission-cli/cmd/spec/spec.go:128, 135, 143`
- `pkg/fission-cli/cmd/spec/validate.go:226, 268`
- `pkg/fission-cli/cmd/support/dump.go:57, 65, 129, 153`
- `pkg/fission-cli/util/util.go:339`

The pattern is again: read each site, classify TP-mechanical vs scanner-FP, refactor TPs, document FPs.

For high-effort completeness, the agent SHOULD do a per-site classification before any code change — exactly as B1 and B2 did. Don't bulk-refactor without classifying.

- [ ] **Step 1: Generate the targeted finding list from SARIF.**

  Run:

  ```bash
  jq -r '.runs[].results[] | select(.ruleId=="file-toctou")
    | "\(.locations[0].physicalLocation.artifactLocation.uri):\(.locations[0].physicalLocation.region.startLine)"' \
    output.sarif | sort -u | grep -v -E '^pkg/(utils|fetcher|builder)/' > /tmp/b3-targets.txt
  cat /tmp/b3-targets.txt
  ```

  Expected: ~26 distinct sites, none in pkg/utils (B1 scope), pkg/fetcher (B2 scope), or pkg/builder (B2 scope).

- [ ] **Step 2: Classify each site.**

  For each site in `/tmp/b3-targets.txt`, read ±10 lines of source.
  Classify as one of:
  - **TP-mechanical**: Stat-then-Open chain → refactor to Open-then-Stat (or `OpenFile` with `O_CREATE|O_EXCL` for create paths).
  - **scanner-FP-stat-only**: `os.Stat` used purely to read mode/exists; no follow-up Open in same fn.
  - **scanner-FP-open-only**: `os.Open` not preceded by Stat in same fn.
  - **scanner-FP-walker**: `filepath.Walk` / `filepath.Glob` / `filepath.Abs` calls flagged due to type heuristic.
  - **leave-with-comment**: known-good mode (e.g., `pkg/logger/logger.go:163` round-1 already at 0o755 for shared-volume reasons) — add inline comment so future scans don't re-trigger investigation.

  Write a one-row entry per site to `.security-fixes/findings-index.md` per-finding section.

- [ ] **Step 3: Refactor each TP site individually.**

  Apply the Open-then-Stat pattern from B2 step 6.
  For create-paths, use `os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)` with mode chosen per round-1 conventions (0o600 for CLI-written outputs, 0o640 for log files, 0o700 for dirs, 0o755 for shared-volume-mounted dirs that need cross-UID read).

  **Per-site mode reference** (use these unless the existing context contradicts; if it does, pin the mode in the index entry):
  - `pkg/fission-cli/cmd/spec/init.go:135` — `os.WriteFile(file, ..., 0644)` writes deployment config → tighten to `0o600` and **use `os.OpenFile` with `O_EXCL`** so a hostile file dropped in cwd can't be silently overwritten.
  - `pkg/fission-cli/cmd/support/dump.go:*` — dump output files → `0o600`. (Round-1 already moved dirs to 0o700.)
  - `pkg/fission-cli/cmd/package/util/util.go:121` — downloaded archive cache → `0o600` (round-1 made package downloads 0o600; verify still in place).
  - `pkg/logger/logger.go:163` — symlink target dir creation; **leave at 0o755** with comment per round-1 finding (tighter modes break shared-volume access).
  - `pkg/logger/logger.go:92` — symlink check in walker; scanner-FP-walker (no actual race — symlink existence check is best-effort; race with concurrent reaper has no security impact).

- [ ] **Step 4: For sites where the existing code uses `os.WriteFile`, switch to `os.OpenFile`-Write-Close** so the mode is materialised at create time, eliminating the small `0644 → 0600` window:

  ```go
  // before
  err := os.WriteFile(file, contents, 0o600)

  // after
  f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
  if err != nil { return err }
  defer f.Close()
  if _, err := f.Write(contents); err != nil { return err }
  return f.Close()
  ```

  Apply this idiom anywhere this batch's TP sites use `os.WriteFile` for security-sensitive content.

- [ ] **Step 5: Run the full test suite for changed packages.**

  Compute the set:

  ```bash
  git diff --name-only main | xargs -n1 dirname | sort -u | sed 's|^|./|' > /tmp/b3-pkgs.txt
  cat /tmp/b3-pkgs.txt
  ```

  Then:

  ```bash
  go test -race -count=1 $(cat /tmp/b3-pkgs.txt | grep '^\./pkg/' | tr '\n' ' ')
  golangci-lint run $(cat /tmp/b3-pkgs.txt | grep '^\./pkg/' | tr '\n' ' ')
  ```

  Expected: PASS for all.

- [ ] **Step 6: Run `make test-run`.**

  Run: `make test-run`.
  Expected: PASS.

- [ ] **Step 7: Update findings index + progress ledger.**

  Per-site rows updated in Step 2 already; flip status (`fixed-<sha>` / `accepted-fp-<reason>`) for each.
  Flip B3 in `progress.md` to `done`.

- [ ] **Step 8: Commit.**

  ```bash
  git add cmd/ pkg/executor/ pkg/featureconfig/ pkg/logger/ pkg/fission-cli/ .security-fixes/
  git commit -m "Open-then-stat refactor across cmd, executor, logger, CLI (B3)"
  ```

---

### B4 — File-mode tightening (loose-file-permissions)

**Files (in scope only — round-2 SARIF cited 6 sites; 4 are in scope):**
- `pkg/fission-cli/cmd/spec/init.go:73` (`os.MkdirAll(specDir, 0755)`)
- `pkg/fission-cli/cmd/spec/init.go:107` (`os.WriteFile(readme, …, 0644)`)
- `pkg/fission-cli/cmd/spec/init.go:135` (`os.WriteFile(file, …, 0644)` — already covered if B3 took it)
- `pkg/logger/logger.go:163` (`os.Mkdir(fissionSymlinkPath, 0755)`)
- `test/benchmark/picasso.go:65, 165` — out of scope per agreed scope (test infra)

If B3 already addressed `init.go:135`, dedupe; otherwise apply here.

- [ ] **Step 1: Tighten `init.go:73` spec dir mode.**

  In `pkg/fission-cli/cmd/spec/init.go`, change:

  ```go
  err := os.MkdirAll(specDir, 0755)
  ```

  To:

  ```go
  // 0o700: spec dir contains deployment config that may carry cluster
  // identifiers; user-private by default. The CLI runs as the user, so
  // no other UID needs to read this.
  err := os.MkdirAll(specDir, 0o700)
  ```

- [ ] **Step 2: Tighten `init.go:107` readme write.**

  Replace:

  ```go
  err := os.WriteFile(readme, []byte(SPEC_README), 0644)
  ```

  With:

  ```go
  err := os.WriteFile(readme, []byte(SPEC_README), 0o600)
  ```

  (Mode-only change; `os.WriteFile` already truncates atomically.)

- [ ] **Step 3: Tighten `init.go:135` deployment-config write — only if B3 didn't already.**

  If B3 already replaced this with `os.OpenFile`-Write-Close at 0o600, skip.
  Otherwise:

  ```go
  err = os.WriteFile(file, append(msg, y...), 0o600)
  ```

- [ ] **Step 4: `pkg/logger/logger.go:163` — verify the round-1 reasoning still applies.**

  The current code:

  ```go
  err = os.Mkdir(fissionSymlinkPath, 0755)
  ```

  Round 1 documented that 0o755 is required for cross-container access on shared volumes (see CLAUDE.md "Things that bite" + `.claude/skills/debug-github-ci/resources/shared-volume-permissions.md`).
  This Mkdir is for the *fission* node-local symlink dir under `/var/log` — NOT a /packages-shared-volume mount.

  Re-read the surrounding code (lines 140-170) to determine whether the symlink reaper or external readers (kubelet log-aggregator, journald) need read access via a different UID.

  **Default decision:** if the only readers are the same UID as the writer, change to `0o700`. If kubelet or another node-side process reads here, leave 0o755 with an inline comment citing the consumer.

  ```go
  // before
  err = os.Mkdir(fissionSymlinkPath, 0755)

  // if external readers (kubelet, log-aggregator) need access — keep 0o755 with comment:
  // 0o755 required for kubelet log-aggregator (separate UID) to traverse this dir.
  err = os.Mkdir(fissionSymlinkPath, 0o755)

  // if user-only:
  err = os.Mkdir(fissionSymlinkPath, 0o700)
  ```

  Document the chosen decision and reasoning in the index entry for this site.

- [ ] **Step 5: Run code-checks + targeted tests.**

  Run:

  ```bash
  golangci-lint run ./pkg/fission-cli/... ./pkg/logger/...
  go test -race -count=1 ./pkg/fission-cli/cmd/spec/... ./pkg/logger/...
  ```

  Expected: PASS.

- [ ] **Step 6: Run `make test-run`.**

  Run: `make test-run`.
  Expected: PASS.

- [ ] **Step 7: Update findings index + progress ledger.**

  Mark each addressed `loose-file-permissions` row in `.security-fixes/findings-index.md` as `fixed-<sha>`.
  Mark `test/benchmark/picasso.go:*` rows as `accepted-out-of-scope-test-infra`.
  Flip B4 in `progress.md` to `done`.

- [ ] **Step 8: Commit.**

  ```bash
  git add pkg/fission-cli/cmd/spec/init.go pkg/logger/logger.go .security-fixes/
  git commit -m "Tighten loose file permissions in spec init and logger (B4)"
  ```

---

### B5 — Index finalisation + pre-PR scrub

**Files:**
- Update: `.security-fixes/findings-index.md` (final pass)
- Update: `.security-fixes/progress.md` (flip remaining batches to done)
- Delete (in pre-PR scrub commit): `.security-fixes/` workspace folder

This batch produces two commits: one finalising the index (kept on the branch), and one scrub commit removing the workspace immediately before the PR opens.

- [ ] **Step 1: Run the SARIF differ.**

  Re-run:

  ```bash
  .security-fixes/sarif-summary.sh output.sarif
  ```

  Expected: same output as Step 3 of B0 (we did not re-scan).

  Optionally — if the user has re-run the scanner since B0 — do this manually:

  ```bash
  # Diff the per-rule counts; should show reductions where B1-B4 fixed sites.
  diff <(jq -r '...' output.sarif) <(jq -r '...' output.sarif.previous)
  ```

  Skip if no re-scan happened.

- [ ] **Step 2: Final accepted-risk register pass.**

  In `.security-fixes/findings-index.md`, ensure the accepted-risk register has one paragraph per category (rationale + the boundary that justifies it), each one matching the seed list in B0 step 4.6. Re-read each entry once for consistency — no contradictions, no placeholders.

- [ ] **Step 3: Cross-check progress ledger against `git log`.**

  Run:

  ```bash
  git log --oneline main..security-fixes-2026-05 | grep -E '\((B[0-9])\)'
  ```

  Expected: B0..B4 each present, in order. Update `.security-fixes/progress.md` SHA columns to match.

- [ ] **Step 4: Commit the finalisation.**

  ```bash
  git add .security-fixes/
  git commit -m "Finalise security-fixes-2026-05 findings index and ledger (B5)"
  ```

- [ ] **Step 5: Pre-PR scrub.**

  Round-1 convention: workspace artefacts do not land in the PR; only source-code changes do.

  Use `git rm` with `git restore --staged` chains to selectively scrub:

  ```bash
  git rm -r .security-fixes/
  # If output.sarif accidentally got staged at any point, also drop it.
  git rm --cached output.sarif 2>/dev/null || true
  git commit -m "Drop .security-fixes/ workspace pre-PR (B5 scrub)"
  ```

  Verify nothing else was unintentionally added:

  ```bash
  git diff main..security-fixes-2026-05 --name-only | sort
  ```

  Expected: only files actually touched by B1-B4 (`pkg/utils/*`, `pkg/fetcher/*`, `pkg/builder/*`, `cmd/*`, `pkg/executor/util/*`, `pkg/featureconfig/*`, `pkg/logger/*`, `pkg/fission-cli/...` — and matching test files); no `.security-fixes/`, no `output.sarif`, no `oci/` or `rfc/` changes.

- [ ] **Step 6: Push the branch and open the PR.**

  ```bash
  git push -u origin security-fixes-2026-05
  gh pr create --title "Security fixes round 2: TOCTOU + log-forging + file modes" --body "$(cat <<'EOF'
  ## Summary
  - Atomic-mode-on-create for HTTP-handler download paths in `pkg/utils` and zip extract.
  - Open-then-stat refactor for `pkg/fetcher` and the cross-package TOCTOU sweep.
  - CR/LF sanitisation of build-script stdout in `pkg/builder.build`.
  - File-mode tightening for `pkg/fission-cli/cmd/spec/init.go` and `pkg/logger/logger.go`.

  Findings index for round 2 with full TP/FP/accepted-risk reasoning is maintained out-of-tree (matches PR #3361 convention) — happy to share if reviewers want it.

  ## Test plan
  - [x] `make code-checks` passes
  - [x] `make test-run` passes
  - [x] Targeted `go test -race -count=1 ./pkg/{utils,fetcher,builder,logger,fission-cli/...}/...` passes
  - [x] `helm lint charts/fission-all` clean (no chart changes in this PR)
  - [ ] CI green on PR

  🤖 Generated with [Claude Code](https://claude.com/claude-code)
  EOF
  )"
  ```

- [ ] **Step 7: Monitor CI.**

  Use the `debug-github-ci` skill's monitor-poll-loop recipe (`Monitor` tool, 30s poll, timeout 40 min).
  If CI flips red, return to Phase 1 of `debug-github-ci` — read logs, classify, fix, push.

---

## Verification recipe (run at every batch close)

```bash
make code-checks                          # golangci-lint
make test-run                             # go test -race ./... with envtest
git status --short                        # only files in the batch's scope?
.security-fixes/sarif-summary.sh output.sarif        # spot-check that no new categories appeared
```

If any step fails:
- **lint**: read the report and fix in the same batch.
- **tests**: read the failing test source first (per `feedback_read_source_before_iterating.md`), fix the underlying issue, retry once.
- **unscoped files in `git status`**: investigate before committing — leftover edits or generated files.

After **all** batches land:
- Re-run the security scanner if available, regenerate `output.sarif`, run the diff.
- Update `.security-fixes/findings-index.md` final table with post-fix counts and commit SHAs.

---

## Resumability and re-entry

A worker resuming this plan should:
1. `git switch security-fixes-2026-05` (or create from main if branch was lost).
2. Read `.security-fixes/progress.md` — the row marked `pending` after the last `done` is the next batch.
3. Read `.security-fixes/findings-index.md` to confirm which sites that batch closes.
4. Read `git log --oneline main..security-fixes-2026-05 | grep -E '\((B[0-9])\)'` to confirm which batch commits are present.
5. Pick the next pending batch in §3's table; do NOT start a new batch until the current one is committed and verified.

---

## Out of scope (explicit)

- **`pkg/plugin/*`** — CLI plugin loader; the 4 `warning-sink-execute` findings here are FP per round-1 triage and confirmed by user in round-2 brainstorm.
- **All `idor-*` findings** — architectural; NetworkPolicy mitigation already in tree from PR #3361, RFC 0001 HMAC implementation deferred indefinitely.
- **`go-tls-skip-verify` in `test/e2e/framework/framework.go`** — test code.
- **`test/integration/testdata/*` Secret Keyword findings** — fixture files; documented at rule level.
- **Re-running the scanner** — user runs the scan; we consume `output.sarif`.
- **`pkg/apis/core/v1/zz_generated.deepcopy.go`, `pkg/apis/core/v1/const.go`** — generated or constant files; cannot be edited directly.
- **Helm chart Secret Keyword findings (3 sites)** — Helm template placeholders, not real secrets.

---

## Critical files (single-line index)

- `output.sarif` — input (gitignored)
- `.security-fixes/findings-index.md` — canonical triage index (B0)
- `.security-fixes/progress.md` — batch ledger (B0)
- `.security-fixes/plan.md` — copy of this plan (B0)
- `.security-fixes/sarif-summary.sh` — query helper (B0)
- `pkg/utils/utils.go` — B1
- `pkg/utils/zip.go` — B1
- `pkg/fetcher/fetcher.go` — B2
- `pkg/builder/builder.go` — B2
- `cmd/builder/main.go`, `cmd/fetcher/app/server.go`, `cmd/fission-bundle/mqtrigger/mqtrigger.go` — B3
- `pkg/executor/util/util.go`, `pkg/featureconfig/config.go`, `pkg/logger/logger.go` — B3
- `pkg/fission-cli/cmd/{package,spec,support}/...`, `pkg/fission-cli/util/util.go` — B3
- `pkg/fission-cli/cmd/spec/init.go`, `pkg/logger/logger.go` — B4
