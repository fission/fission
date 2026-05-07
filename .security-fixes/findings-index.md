# Fission Security Findings Index — Round 2

**Scan tool:** Harness SAST and SCA 1.0.0
**Scan date:** 2026-05-07
**Source SARIF:** `output.sarif` (gitignored at repo root; `*.sarif` rule in `.gitignore`)
**Totals:** 218 findings — 20 errors / 94 warnings / 104 notes

This is the canonical triage of every finding in the round-2 SARIF.
Each rule is classified once; the per-finding section enumerates only true positives that need a code change.
False positives, accepted-risk, and architectural-deferred items are documented at rule level and in the accepted-risk register at the end.

For round-1 history (PR #3361), see `git log -- pkg/builder/builder.go` and the commits between `4982e27d` (last pre-round-1 commit) and `5a3d68a3` (round-1 merge).

---

## Round-2 deltas vs round-1

| Rule | Round 1 | Round 2 | Closed by |
|---|---|---|---|
| `directory-traversal-http` | 4 | **0** | PR #3361 — `Builder.Clean` `SanitizeFilePath` |
| `oss_vuln` (CVE-2026-26958) | 1 | **0** | PR #3361 — `filippo.io/edwards25519` v1.1.0 → v1.2.0 |
| `loose-file-permissions` | 11 | 6 | PR #3361 — 5 sites tightened (CLI dump dir 0o700, fragments 0o600, package downloads 0o600) |
| `file-toctou` | 76 | 72 | PR #3361 — 4 sites refactored (storagesvc/client `Upload`/`Download` open-then-fstat / `O_EXCL`) |
| `timing-password-compare` | 11 | 10 | PR #3361 — `pkg/router/auth.go` constant-time compare |

Everything else carried forward unchanged.
The full round-2 baseline is below.

---

## Quick query examples

The helper `.security-fixes/sarif-summary.sh` wraps the common `jq` patterns.

```bash
# Whole-SARIF summary (severity, per-rule CVSS-ordered, top folders)
.security-fixes/sarif-summary.sh output.sarif

# All distinct sites for a rule
.security-fixes/sarif-summary.sh output.sarif file-toctou

# Filter by package + rule
jq -r '.runs[].results[]
  | select(.ruleId=="file-toctou")
  | "\(.locations[0].physicalLocation.artifactLocation.uri):\(.locations[0].physicalLocation.region.startLine)"' \
  output.sarif | grep '^pkg/utils/'
```

---

## Per-rule index

| Rule | CVSS | Count | Verdict | Batch | Reasoning |
|---|---|---|---|---|---|
| `command-injection-attacker-controlled` | 9 | 4 | accepted-risk | — | Scanner re-flag of round-1 allowlist; `Builder.resolveBuildCommand` validates absolute path with no whitespace metas (`pkg/builder/builder.go`) |
| `warning-sink-execute` | 8.5 | 6 | mixed-OOS | — | 4 in `pkg/plugin/*` (out of scope per round-2 user decision); 2 in `pkg/builder/builder.go:254` are a duplicate flag of the cmd-injection allowlist |
| `warning-sink-html` | 8 | 10 | FP | — | Every cited handler sets `Content-Type: application/json` before writing JSON marshalled output; no HTML sink |
| `Secret Keyword` (per-file rule IDs) | 5.5 | ~50 | FP | — | Flag/const/struct-field names matching keyword heuristics; `go.sum` hashes; Helm template placeholders. Reviewed once at rule level, not per-finding. |
| `idor-cloud-access` | 5 | 1 | architectural-deferred | — | NetworkPolicy mitigation in tree (`charts/fission-all/templates/networkpolicy.yaml`); RFC 0001 HMAC application-layer auth deferred indefinitely |
| `idor-collection-access` | 4 | 2 | architectural-deferred | — | Same architectural cluster |
| `idor-pattern-based` | 3 | 2 | architectural-deferred | — | Same architectural cluster |
| `idor-external-api` | 3 | 5 | architectural-deferred | — | Same architectural cluster |
| `go-tls-skip-verify` | 5 | 1 | accepted-out-of-scope | — | Only remaining site is `test/e2e/framework/framework.go:144` (test infrastructure); kafka site closed in round-1 PR #3361 |
| `sleep-from-attacker-controlled` | 4.5 | 18 | FP | — | Every alias is a constant `time.Sleep` in CLI poll loops; no attacker-controlled sleep |
| `timing-password-compare` | 3 | 10 | FP | — | Every flagged compare is on `ResourceVersion` / namespace / container-config fields used by reconcile loops to decide whether to roll out; not credential comparison. Per-site rationale below. |
| `file-toctou` | 3 | 72 | mixed-mechanical | B1/B2/B3 | Per-site classification — see per-finding rows below |
| `log-forging-attacker` | 2.5 | 2 | TP | B2 | `pkg/builder/builder.go:298` streams attacker-controlled build-script stdout to logs without CR/LF sanitisation |
| `http-to-log` | 2.5 | 2 | FP | — | `pkg/fission-cli/console/log.go:55` (`Verbose`) already sanitises via `sanitizeLogLine` (round 1); scanner re-flags because source is tainted, but sink is clean |
| `time-file` | 2 | 4 | FP | — | `time.Now().UnixNano()` used as a filename suffix in `pkg/fission-cli/cmd/support/dump.go`; not a security boundary |
| `loose-file-permissions` | 1 | 6 | mixed-TP | B4 | 4 sites in scope (`pkg/fission-cli/cmd/spec/init.go` × 3, `pkg/logger/logger.go:163`); 2 in `test/benchmark/picasso.go` are out of scope |
| `http-no-ssl` | 1 | 1 | accepted-risk | — | Internal cluster service; TLS terminated by ingress |

---

## Per-finding rows (true positives only)

Populated as fixes land in batches B1–B4.

| File:Line | Function | Rule | Batch | Status |
|---|---|---|---|---|
| `pkg/utils/utils.go:193` | `DownloadUrl` | file-toctou | B1 | fixed-e68db701 (atomic mode-on-create via `os.OpenFile` 0o600) |
| `pkg/utils/zip.go:45` | `MakeZipArchiveWithGlobs` | file-toctou | B1 | fixed-e68db701 (atomic mode-on-create via `os.OpenFile` 0o600) |
| `pkg/utils/zip.go:106` | `Unarchive` | file-toctou | B1 | fixed-e68db701 (atomic mode-on-create via `os.OpenFile` with archive entry mode) |
| `pkg/utils/utils.go:78,82` | `FindAllGlobs` | file-toctou | — | accepted-fp (filepath.Abs/Glob — no file open) |
| `pkg/utils/utils.go:120` | `FileSize` | file-toctou | — | accepted-fp (Stat-only; no follow-up Open in same fn) |
| `pkg/utils/utils.go:128` | `GetFileChecksum` | file-toctou | — | accepted-fp (Open-then-Read; no Stat-before-Open) |
| `pkg/utils/utils.go:284,288` | `DeleteOldPackages` | file-toctou | — | accepted-fp (RemoveAll on validated /packages prefix) |
| `pkg/utils/zip.go:14` | `IsZip` | file-toctou | — | accepted-fp (Open-then-Match; no race) |
| `pkg/utils/zip.go:56` | `MakeZipArchiveWithGlobs` | file-toctou | — | accepted-fp (filepath.Abs return value — no file op) |
| `pkg/utils/zip.go:62` | `Archive` | file-toctou | — | accepted-fp (Stat→IsDir branch; no follow-up Open of src) |
| `pkg/utils/zip.go:76` | `Unarchive` | file-toctou | — | accepted-fp (Open-then-Extract; no Stat-before-Open) |
| `pkg/utils/zip.go:86,94` | `Unarchive` extract loop | file-toctou | — | accepted-fp (MkdirAll over fresh dest path; round-1 0o755 retained for shared-volume access) |
| `pkg/builder/builder.go:298` | `Builder.build` | log-forging-attacker | B2 | fixed-pending-sha (CR/LF escape via `sanitizeBuildLogLine`) |
| `pkg/builder/builder.go:311` | `Builder.build` cmdErr log | log-forging-attacker | — | accepted-fp (cmdErr wraps allowlisted command name from `resolveBuildCommand`) |
| `pkg/fetcher/fetcher.go:74` | `makeVolumeDir` | file-toctou | — | accepted-fp (MkdirAll on volume root; no Stat-then-Open chain) |
| `pkg/fetcher/fetcher.go:262` | `Fetch` storePath check | file-toctou | — | accepted-fp (Stat-existence-check; follow-up is write to different path `tmpPath`) |
| `pkg/fetcher/fetcher.go:315` | `Fetch` literal write | file-toctou | — | accepted-fp (`os.WriteFile` is atomic with mode 0o600) |
| `pkg/fetcher/fetcher.go:549` | `Upload` archive rename | file-toctou | — | accepted-fp (`os.Rename` is atomic) |
| `pkg/fetcher/fetcher.go:603` | `Fetcher.rename` | file-toctou | — | accepted-fp (`os.Rename` is atomic) |
| `cmd/*`, `pkg/executor/util/util.go`, `pkg/featureconfig/config.go`, `pkg/logger/logger.go`, `pkg/fission-cli/cmd/{spec,support,package}/*` | (per B3 Step 2 classification) | file-toctou | B3 | open |
| `pkg/fission-cli/cmd/spec/init.go:73` | `InitSubCommand.run` (specDir mode) | loose-file-permissions | B4 | open |
| `pkg/fission-cli/cmd/spec/init.go:107` | `InitSubCommand.run` (readme write) | loose-file-permissions | B4 | open |
| `pkg/fission-cli/cmd/spec/init.go:135` | `writeDeploymentConfig` | loose-file-permissions | B4 | open |
| `pkg/logger/logger.go:163` | `Start` (fissionSymlinkPath mkdir) | loose-file-permissions | B4 | open |

After each batch lands, replace `open` with `fixed-<short-sha>`.
Sites that turn out to be scanner-FP after classification get an `accepted-fp-<one-line-reason>` row instead.

---

## Accepted-risk register

Each paragraph is the canonical justification for not changing code in response to a finding category.

### `command-injection-attacker-controlled` (4)

The four findings all point at `pkg/builder/builder.go:254` where `Builder.build` calls `exec.Command(buildCmd, buildArgs...)`.
The build command is allowlisted by `Builder.resolveBuildCommand` (added in PR #3361): it accepts an empty string (default `/build`), the literal `/build`, or any absolute path that survives `filepath.Clean` and contains no `..`.
Whitespace splits via `strings.Fields`, which means there is no shell expansion and `exec.Command` does not invoke a shell.
The scanner cannot see the validation function, so it re-flags every taint flow that reaches `exec.Command`.
This is a known false positive after the round-1 mitigation.

### `warning-sink-execute` (6)

Four findings are in `pkg/plugin/*` (CLI plugin loader at `pkg/plugin/plugin.go:93,155`).
The user explicitly excluded `pkg/plugin/*` from round-2 scope; the plugin loader executes user-installed binaries by design, and the load path validates the filename is in `$PATH`/`$FISSION_PLUGIN_DIR`.
The remaining two findings flag `pkg/builder/builder.go:254`, which is a duplicate of the `command-injection-attacker-controlled` cluster — same allowlisted call, different rule.

### `warning-sink-html` (10)

Every cited handler writes a JSON-marshalled body to `http.ResponseWriter` after explicitly setting `Content-Type: application/json`.
Browsers do not interpret JSON bodies as HTML in this content-type, so reflected user input cannot be executed as script.
The scanner heuristic flags any path from request input to `w.Write`, regardless of content type.

### `idor-*` (10 across four sub-rules)

The IDOR cluster all lands at the same architectural surface: `pkg/storagesvc`, `pkg/fetcher`, `pkg/builder`, and a few `pkg/router`/`pkg/utils` helper sites that touch caller-supplied IDs.
None of these endpoints have application-layer authentication today.
Round 1 shipped a `NetworkPolicy` (`charts/fission-all/templates/networkpolicy.yaml`) that restricts cross-pod traffic to executor and buildermgr peers, with cross-namespace pod selectors (`namespaceSelector: {}`).
The `kind-ci` skaffold profile enables it on every CI run via a `replace` patch on `setValues`.
The long-term application-layer plan — HMAC over `(method, path, sha256(body), unix_minute)` headers — is captured in RFC 0001 (drafted in round 1, deferred from PR #3361 by user decision).
This round does not implement RFC 0001.

### `go-tls-skip-verify` (1)

Round 1 closed the `pkg/mqtrigger/messageQueue/kafka/kafka.go` site by failing closed on a malformed `INSECURE_SKIP_VERIFY` value and emitting a warn log when it is set to `true`.
The only round-2 site is `test/e2e/framework/framework.go:144`, which sets `InsecureSkipVerify: true` for the e2e test runner's HTTP client when probing services.
Test code is out of scope; production paths are unaffected.

### `sleep-from-attacker-controlled` (18)

Every alias resolves to a constant `time.Sleep` inside CLI poll loops — most prominently `pkg/fission-cli/cmd/spec/buildwatch.go:116` where the CLI polls package build status.
The literal `2 * time.Second` is hard-coded in source; the rule fires because the surrounding loop is fed by remote status, but no attacker-controlled value reaches the `Sleep` argument.

### `timing-password-compare` (10) — per-site rationale

Each cited equality compare is on internal Kubernetes object state used by reconciliation, not on credentials.

- `pkg/executor/cms/secrethandler.go:43` — secret `ResourceVersion` diff to decide whether to re-fetch and re-mount; not credential.
- `pkg/webhook/function.go:59` — admission validation of function name string; rejects empty / malformed names.
- `pkg/executor/executortype/newdeploy/newdeploymgr.go:621,625` — function spec field equality (image, env, resources) for rollout decision; not credential.
- `pkg/mqtrigger/scalermanager.go:78,260` — trigger field equality (topic, message-source, KEDA scaler config) for state diff; not credential.
- `pkg/executor/executortype/container/containermgr.go:567,571` — container executor config equality for rollout decision; not credential.
- `cmd/preupgradechecks/checks.go:152` — namespace string compare; not credential.

### `time-file` (4)

`pkg/fission-cli/cmd/support/dump.go` uses `time.Now().UnixNano()` to suffix the dump archive filename so multiple invocations don't collide.
The result is not used as a token, identifier, or any other security primitive.

### `http-no-ssl` (1)

Internal Fission services (`storagesvc`, `fetcher`, `executor`, `builder`) communicate over plain HTTP within the cluster.
Cluster operators are expected to terminate TLS at the ingress (Nginx / Istio / GKE LB) and rely on Kubernetes network policies for inter-pod isolation.
The round-1 NetworkPolicy hardens the inter-pod surface; per-service mTLS is not on the round-2 roadmap.

### `Secret Keyword` (~50, per-file rule IDs)

The rule fires per-file based on a keyword heuristic and produces ~50 findings across `pkg/fission-cli/flag/*`, `pkg/fission-cli/cmd/*`, `charts/fission-all/templates/*`, `test/integration/testdata/go/module_example/go.sum.txt`, etc.
None of the matches are real secrets — every hit is a flag name (`--secret`, `--token`), constant identifier, struct-field name, or Helm placeholder.
Reviewed once at rule level; per-finding triage was rejected as not cost-effective.

---

## Re-scan workflow

When a fresh `output.sarif` arrives:

1. Re-run the scanner; place the new SARIF at the repo root.
2. Run `.security-fixes/sarif-summary.sh output.sarif` and compare the per-rule table against the **Per-rule index** above.
3. Any new `ruleId` that isn't in the table, or a count delta on a known rule, is real signal — re-triage that delta.
   Counts that match are already classified; trust the existing verdict unless a per-finding row explicitly says otherwise.
