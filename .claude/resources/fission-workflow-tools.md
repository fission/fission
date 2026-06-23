# Fission workflow tool inventory

Repo-specific input to the generic `bump-ci-tool-versions` skill: which workflows pin which CLI tools, the deliberately-stale pins, and the gotchas behind them.
Re-discover dynamically (`grep -rnE "^\s*[A-Z_]+_VERSION:" .github/workflows/`) rather than trusting this snapshot; it's for orientation.

## Pinned `*_VERSION:` env vars (snapshot)

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

SHA-pinned actions usually present (Dependabot's domain): `Azure/setup-helm`, `goreleaser/goreleaser-action`, `sigstore/cosign-installer`, `aquasecurity/trivy-action`, `helm/kind-action`, `step-security/harden-runner`, `actions/checkout`, `actions/setup-go`.

## Deliberately-stale pins — don't "fix"

- **Helm v3 in `upgrade_test.yaml` vs v4 in `push_pr.yaml`** is intentional: `upgrade_test` simulates a user on Helm v3 upgrading their Fission install.
  Leave the v3 pin unless the user says otherwise.
- **`matrix.kindversion` entries that intentionally lag** (e.g. testing an older k8s LTS for backwards compat) — leave unless the kind binary no longer ships that `kindest/node:vX.Y.Z` image.

## Gotchas

- **`KIND_VERSION` (binary) ≠ `matrix.kindversion` (node image tag).**
  `KIND_VERSION` is the kind CLI from `kubernetes-sigs/kind`; `matrix.kindversion` is the `kindest/node:vX.Y.Z` k8s node image.
  They version separately — but the kind binary must still support the requested node-image tag (check the kind release notes' "Supported Kubernetes versions" table).
- **cosign is installed twice in `release.yaml`** (the release job and `image-provenance-verification-with-cosign`).
  Both read `COSIGN_VERSION`, so one bump covers both — but confirm both still reference the env var, not a hardcoded version, after editing.
- **Skaffold breaking config changes.**
  Skaffold occasionally renames/removes profile keys.
  `skaffold.yaml` uses the `kind`, `kind-ci`, `kind-debug`, `kind-opentelemetry` profiles (and a `kind-ci-old` mirror).
  After a bump, `skaffold diagnose --profile kind-ci` against the repo, and check `apiVersion`/`kind: Config` match the new schema.
- **golangci-lint config schema migrations.** v2.0.0 and v2.x.0 minors have repeatedly tightened `.golangci.yaml`.
  On a config-validation failure, `golangci-lint config verify` against the new binary and migrate the deprecated linter name — don't `--no-config` your way out.

## Out of scope (matches the generic skill)

Tools versioned in `Makefile`/`hack/`/`Dockerfile`/goreleaser config (they live with the build code, not the runners); floating constraints (`with: version: "~> v2"`); major version bumps (dedicated branch).
