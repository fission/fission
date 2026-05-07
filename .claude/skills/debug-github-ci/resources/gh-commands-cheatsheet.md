# `gh` CLI cheatsheet for CI debugging

Every `gh` command we use during a CI debug session, ordered by frequency. Copy-paste oriented.

## See PR check states

```bash
# All checks — bucket is one of: pass | fail | pending | skipping
gh pr checks <PR> --json name,bucket,state,link \
  --jq '.[] | select(.name != null) | "\(.bucket)\t\(.name)\t\(.link)"'

# Just failures
gh pr checks <PR> --json name,bucket,link \
  --jq '.[] | select(.bucket=="fail") | "\(.name)\t\(.link)"'

# Summary count
gh pr checks <PR> --json name,bucket \
  --jq '.[] | select(.name != null) | "\(.bucket)\t\(.name)"' | sort | uniq -c | head
```

## Compare PR against `main`

```bash
# Recent main runs — same workflow as PR
gh run list --branch main --workflow=push_pr.yaml --limit 5 \
  --json conclusion,headSha,databaseId,createdAt

# Status checks for a specific commit on main (FOSSA, codecov, etc.)
gh api repos/fission/fission/commits/<sha>/status \
  --jq '.statuses[] | {context, state, description}'
```

If a check is `error`/`failure` on main with the same `description` as your PR, it's pre-existing noise.

## Get logs

```bash
# Cheapest — failed steps only (works after run completes)
gh run view <runId> --log-failed 2>&1 | head -200

# Per-job logs — works mid-run, useful when other jobs are still pending
gh api repos/fission/fission/actions/jobs/<jobId>/logs 2>&1 \
  | grep -B2 -A8 'FAIL: Test\|error|denied|i/o timeout' | head -60

# Full run log — ONLY if cheaper paths failed; tens of MB
gh run view <runId> --log
```

`<jobId>` comes from the `link` field in `gh pr checks` output: `https://github.com/fission/fission/actions/runs/<runId>/job/<jobId>`.

## Download artifacts

```bash
# List artifacts on a run
gh api repos/fission/fission/actions/runs/<runId>/artifacts \
  --jq '.artifacts[] | {name, size_in_bytes}'

# Download by name (prefer this over the URL form)
rm -rf /tmp/ci-logs && gh run download <runId> \
  -n go-integration-logs-<runId>-v<k8s-version> -D /tmp/ci-logs
ls /tmp/ci-logs/
```

The interesting artifacts on a Fission integration test job:
- `go-integration-logs-<runId>-v<k8s>` — per-test diag dirs (`testbuildermgr/`, `testkubectlapply/`, etc.) with pod logs and yamls
- `kind-logs-<runId>-v<k8s>` — node-level kubelet/containerd logs and `fission` namespace pod logs
- `prom-dump-<runId>-v<k8s>` — Prometheus snapshot

## Find Dependabot alerts

```bash
# Open alerts summary
gh api 'repos/fission/fission/dependabot/alerts?state=open&per_page=100' \
  --jq '.[] | "\(.security_advisory.severity)\t\(.security_advisory.ghsa_id)\t\(.dependency.package.name)\t\(.security_advisory.summary)"'

# With CVE IDs and fix versions
gh api 'repos/fission/fission/dependabot/alerts?state=open&per_page=100' \
  --jq '.[] | "\(.security_advisory.severity)\t\(.security_advisory.ghsa_id)\t\(.dependency.package.name)\tfixed_in: \(.security_advisory.vulnerabilities[0].first_patched_version.identifier // "?")"'
```

Common surprise: alert lists "fixed_in: 2.0.0-beta.8" for `github.com/docker/docker` because the fix is at the new `github.com/moby/moby/v2` import path, not the old one. Bumping `docker/cli` to v29.x usually pulls the fix in transitively and drops `docker/docker` from go.mod.

## PR-level operations

```bash
# Open a PR
gh pr create --title "<title>" --body "$(cat <<'EOF'
## Summary
- ...
## Test plan
- [x] ...
🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"

# Inspect a PR's view
gh pr view <PR> --json number,title,state,mergeable,statusCheckRollup
```

## Cancelling a run

```bash
gh run cancel <runId>
```

Rarely needed — usually it's faster to push a fix and let the new run supersede.

## Repository conventions for this repo

- Workflow file: `.github/workflows/push_pr.yaml` (Fission CI), plus `codeql.yaml`, `dependency-review.yml`, `lint.yaml`, etc.
- The PR template lives at `.github/PULL_REQUEST_TEMPLATE.md` — Fish doesn't use a `## Test plan` section convention by default; following the template form is preferred for human reviewers but Claude-generated PRs typically still include it for traceability.
- License Compliance via `app.fossa.com` is configured but is currently red on `main` with "11 issues found" — pre-existing.
