---
name: workflow-tool-versions
description: Bump pinned CLI tool versions in GitHub Actions workflows on the Fission repo (helm, kind, skaffold, cosign, golangci-lint, goreleaser, etc.) — primarily the `*_VERSION:` env vars and the `# vX.Y.Z` comments next to SHA-pinned `uses:` actions. Use when the user asks to update workflow tool versions, do a CI tool sweep, check what's outdated in `.github/workflows/`, or process Dependabot's grouped github-actions PR.
---

# Workflow tool-version sweep (Fission)

Playbook for refreshing the CLI tool versions that the Fission GitHub Actions workflows pin. Optimised for **isolating regressions**: each tool lands as its own commit so CI failure attribution is one click on `git blame`.

## When to invoke

Trigger phrases: "update workflow tool versions", "bump skaffold/kind/cosign/helm", "what's outdated in our workflows", "process the dependabot github-actions PR", "CI tool refresh".

Skip if the user names a single tool and a single version — just edit and commit.

## Scope (in order of priority)

1. **Primary — `env:` block `*_VERSION:` vars.** These are the versions the workflow downloads at runtime. This is the user's main scope.
2. **Secondary — `# vX.Y.Z` comments next to SHA-pinned `uses:` actions.** Dependabot already does these on a weekly schedule (`.github/dependabot.yml` has a `github-actions` group), so a manual sweep is mostly catch-up if Dependabot is behind.
3. **Tertiary — matrix `kindversion:` (k8s node images for kind).** Tied to kind release support windows; bump only when the underlying kind binary supports newer node images. Verify against `kind` release notes — kind v0.X.Y publishes a list of supported `kindest/node` tags.

Out of scope:
- `with: version: "~> v2"` (floating semver constraints — by design, no pin to bump).
- Tool versions referenced in `Makefile`, `hack/`, `Dockerfile`s, or `goreleaser` config — those live with the build code, not the workflow runners.

## Known inventory (snapshot for orientation)

The skill should re-discover dynamically (see Phase 1) — but a recent snapshot for orientation:

| Workflow | Var | Tool | Upstream repo |
|---|---|---|---|
| push_pr.yaml | HELM_VERSION | helm | `helm/helm` |
| push_pr.yaml | KIND_VERSION | kind | `kubernetes-sigs/kind` |
| push_pr.yaml | SKAFFOLD_VERSION | skaffold | `GoogleContainerTools/skaffold` |
| upgrade_test.yaml | HELM_VERSION | helm | `helm/helm` |
| upgrade_test.yaml | KIND_VERSION | kind | `kubernetes-sigs/kind` |
| release.yaml | KIND_VERSION | kind | `kubernetes-sigs/kind` |
| release.yaml | COSIGN_VERSION | cosign | `sigstore/cosign` |
| lint.yaml | GOLANGCI_LINT_VERSION | golangci-lint | `golangci/golangci-lint` |

SHA-pinned actions usually present:
- `Azure/setup-helm`, `goreleaser/goreleaser-action`, `sigstore/cosign-installer`, `aquasecurity/trivy-action`, `helm/kind-action`, `step-security/harden-runner`, `actions/checkout`, `actions/setup-go`.

## Phase 0 — Baseline

1. Create branch: `git checkout -b ci/tool-versions-<YYYY-MM>` off `main`.
2. Inventory dynamically — do not trust the snapshot above:
   ```bash
   grep -rnE "^\s*[A-Z_]+_VERSION:" .github/workflows/
   ```
   Then for SHA-pinned actions with version comments:
   ```bash
   grep -rnE "uses:.*@[a-f0-9]{40} # v" .github/workflows/
   ```
3. Resolve each pinned tool to its upstream repo (see table) and fetch latest:
   ```bash
   for repo in helm/helm kubernetes-sigs/kind GoogleContainerTools/skaffold sigstore/cosign golangci/golangci-lint; do
     latest=$(gh api "repos/$repo/releases/latest" --jq '.tag_name')
     echo "$repo -> $latest"
   done
   ```
   `gh api` already authenticates; no token wrangling needed.

## Phase 1 — Decide what to bump

Per tool, decide based on the size of the jump:

- **Patch (vX.Y.Z → vX.Y.Z+n)**: take it unconditionally. Read the release notes only if the version skips many patches.
- **Minor (vX.Y → vX.Y+1)**: read upstream release notes for breaking-ish changes — golangci-lint and skaffold periodically rename or drop linters/profiles. Helm minor bumps are usually safe; kind minor bumps may drop support for older `kindest/node` tags, so cross-check against the kindversion matrix.
- **Major (vX → vX+1)**: don't lump into a sweep. Open a separate branch.

Flag deliberate stale pins:
- `upgrade_test.yaml` historically pins **Helm v3.x** while `push_pr.yaml` pins **v4.x** — `upgrade_test` simulates the v3-user upgrade path, so the v3 pin is *intentional*. Leave it unless the user says otherwise.
- `kindversion:` matrix entries that intentionally lag (testing k8s 1.28 LTS for backwards compat) — leave them unless the kind binary no longer ships a `kindest/node:v1.28.x` image.

## Phase 2 — Per-tool workflow

For each tool to bump:

```bash
# Edit the *_VERSION: line(s). If the var is used in >1 workflow,
# bump them in the same commit IFF the workflows share intent
# (push_pr + release both want "latest stable kind"). Keep
# upgrade_test separate when it has its own intent.
$EDITOR .github/workflows/push_pr.yaml .github/workflows/release.yaml

# Sanity-check the YAML still parses (no `yq` required — Python does fine)
python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]" \
  .github/workflows/push_pr.yaml .github/workflows/release.yaml

git add .github/workflows/
git commit -m "Bump <tool> v<old> -> v<new>"
```

Commit message convention (matches Fission's existing dep-bump style):
```
Bump <tool> v<old> -> v<new>

<One line on why if it closes a CVE or unblocks a known issue.>
```

If the tool is in **multiple files with the *same* intent** (e.g. kind across push_pr and release), bump them in one commit. If intents differ (helm in push_pr-target vs upgrade_test-source), separate commits.

## Phase 3 — Verification

There is no local equivalent for "did this workflow still pass" — CI is the test.

1. `git log --oneline main..HEAD` to review the per-tool commits.
2. `git push -u origin <branch>` and let CI run on the PR.
3. If a single tool's commit breaks CI, you can revert that one commit without affecting the rest. That's the point of the per-tool structure.

## Pitfalls

### Helm CLI version drift between push_pr and upgrade_test
`upgrade_test.yaml` pinning Helm v3.x while `push_pr.yaml` is on v4.x looks like a bug; it's deliberate (testing the "user is on Helm v3, upgrading their Fission install" path). Don't "fix" this by aligning them.

### kindversion matrix is NOT the kind binary
`KIND_VERSION` is the kind CLI binary (`kubernetes-sigs/kind`). `matrix.kindversion` is the k8s node image tag (`kindest/node:vX.Y.Z`). They version separately. Updating one does not require updating the other, but the kind binary must still support the requested node-image tag — check the kind release notes' "Supported Kubernetes versions" table.

### Skaffold breaking config changes
Skaffold occasionally renames or removes profile keys. The Fission `skaffold.yaml` uses `kind`, `kind-ci`, `kind-debug`, `kind-opentelemetry` profiles. After a skaffold bump, grep for `apiVersion` and `kind: Config` in `skaffold.yaml` and verify they match the new skaffold's expected schema. Most safe: download the new skaffold locally and run `skaffold diagnose --profile kind-ci` against the repo.

### golangci-lint config schema migrations
`v2.0.0` and `v2.x.0` minors have repeatedly tightened the `.golangci.yaml` schema. If you bump and CI fails with a config-validation error, run `golangci-lint config verify` against the new binary locally. Sometimes the fix is to migrate a deprecated linter name; don't `--no-config` your way out.

### cosign across two workflow jobs in release.yaml
`release.yaml` installs cosign **twice** (once in the release job, once in `image-provenance-verification-with-cosign`). Both read the same `COSIGN_VERSION` env, so a single bump covers both — but verify both still reference the env, not a hardcoded version, after editing.

### Dependabot's grouped github-actions PR
Dependabot already groups *action* version bumps weekly (`.github/dependabot.yml` declares a `github-actions` group). The `*_VERSION:` env vars are NOT touched by Dependabot — those are runtime downloads, not action versions. So this skill's primary scope (env vars) does not overlap with Dependabot's; the SHA-pinned `uses:` bumps usually arrive via Dependabot, so manually doing them is catch-up if Dependabot is paused or behind.

## Out of scope

- Tools versioned in `Makefile`, `hack/`, `Dockerfile`, or goreleaser config.
- Floating semver ranges (`~> v2`).
- Major version bumps — do those on a dedicated branch with their own validation plan.

## Canonical execution (copy-paste outline for the model)

```
1. TaskCreate: baseline + one task per tool to bump + push/CI
2. git checkout -b ci/tool-versions-<YYYY-MM>
3. Discover pinned versions via grep on .github/workflows/
4. For each *_VERSION env var, gh api repos/<owner>/<repo>/releases/latest
5. For each tool to bump (patch/minor; defer majors):
     a. Edit the *_VERSION line(s); group same-intent files together
     b. Validate YAML parses (python3 yaml.safe_load)
     c. Commit per tool
6. git push and let CI verify
7. Summarize: commits on branch, versions bumped, deferred majors, flagged
   intentional stale pins (Helm v3 in upgrade_test).
```
