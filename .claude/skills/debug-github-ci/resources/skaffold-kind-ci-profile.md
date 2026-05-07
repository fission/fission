# Adding a CI-only Helm flag via the `kind-ci` skaffold profile

Some Helm-chart features are off by default for users (backwards compat, sane defaults) but should be **on** in CI to exercise the code path. The pattern in this repo is to flip them via the `kind-ci` skaffold profile.

## When to use this pattern

Examples that already use it:
- `canaryDeployment.enabled: true`
- `podMonitor.enabled: true` / `serviceMonitor.enabled: true`
- `grafana.dashboards.enabled: true`
- `prometheus.serviceEndpoint: <ci value>`
- `storagesvc.archivePruner.interval: 1` (faster GC for tests)
- `networkPolicy.enabled: true` (added 2026-05)

Use it when:
- The feature has a default-off Helm value users opt into.
- Skipping it in CI means a code path is never exercised, allowing regressions.
- Enabling it doesn't break the test framework (verify locally first).

## Two-step pattern (skaffold uses JSONPatch, which requires `add` for missing keys)

### Step 1 — Declare the value with its default in the base `setValues` block

`skaffold.yaml` has a top-level `manifests.helm.releases[0].setValues` map. Every Helm value the profiles can patch must already exist here, with its **chart-default** value:

```yaml
manifests:
  helm:
    releases:
      - chartPath: charts/fission-all
        name: fission
        namespace: fission
        setValues:
          ...
          networkPolicy.enabled: "false"   # <- step 1: add with default
          ...
```

Why: skaffold uses JSONPatch internally for profile patches. JSONPatch `replace` requires the path to exist; `add` would also work but `replace` matches the convention already in this file. By declaring with the default, `replace` semantics match.

If you skip step 1 and try `replace` on a non-existent key, skaffold render fails with `path does not exist` — not always with a friendly error message.

### Step 2 — Add a `replace` patch in the `kind-ci` profile

```yaml
profiles:
  - name: kind-ci
    patches:
      ...existing patches...
      - op: replace
        path: /manifests/helm/releases/0/setValues/networkPolicy.enabled
        value: true
```

Some notes:
- `value: true` is parsed as a YAML boolean, then JSONPatch'd into the string-keyed map. Helm's `--set` accepts both, so this works.
- The path uses dots inside the key segment (`networkPolicy.enabled`) because that's how the base map is keyed — JSONPatch path syntax allows this when the map keys themselves contain dots.

### Step 3 — Verify the render

```bash
helm lint charts/fission-all
helm template charts/fission-all --set networkPolicy.enabled=true \
  | sed -n '/^kind: NetworkPolicy/,/^---/p' | head -50
helm template charts/fission-all --set networkPolicy.enabled=false \
  | grep -c 'kind: NetworkPolicy'   # should print 0
```

For a stronger check (validates skaffold's full pipeline, not just helm):
```bash
skaffold render --profile=kind-ci 2>&1 | grep -E 'kind: NetworkPolicy|networkPolicy'
```
This requires `skaffold` installed locally; CI will catch problems too if you skip it.

## Don't forget to also expose the value to chart users

If the feature should be tunable for users (and not just turned on in CI), add the same value to `charts/fission-all/values.yaml` with a comment explaining what it does and the default. The `kind-ci` patch then just overrides that user-visible default for CI.

If the feature is purely an internal CI knob (no user-facing surface), skip the `values.yaml` entry — the skaffold patch is enough.

## Mirror-check: `kind-ci-old` profile

The repo also has a `kind-ci-old` profile that mirrors `kind-ci` for testing against older Fission releases. If you add a value that should be tested across both, mirror the patch there too. If it's specific to a behaviour only in current `main` (e.g. a NetworkPolicy template that doesn't exist in older charts), `kind-ci` only is correct.
