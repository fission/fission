# Migration Status

Live tracker for the bash → Go integration test migration.
See `00-design.md` for the design; `02-framework-api.md` for helper docs.

## Legend

| Status | Meaning |
|--------|---------|
| `bash-active` | Bash test runs in CI; not yet migrated. |
| `bash-disabled-existing` | Bash test was already `#test:disabled` before this migration began. Triage in Phase 5. |
| `bash-disabled-migrated` | Bash test marked `#test:disabled` because the Go counterpart is live. |
| `go-live` | Go test is in CI and passing. |
| `go-skip` | Go test exists but is `t.Skip`'d (env-gated or known limitation). |
| `deleted` | Bash test was deleted as part of triage; no Go counterpart needed. |

## Summary counters

Update these whenever the table below changes.

- Total bash tests: 48
- In `kind_CI.sh` active list: 19 (9 phase-1, 10 phase-2)
- Not in `kind_CI.sh` active list: 29 (3 never were + 26 migrated)
- `bash-active`: 11
- `bash-disabled-existing`: 6
- `bash-disabled-migrated`: 31 (+ python_env, nodejs_env, env_podspec, function_timeout)
- `go-live`: 30
- `go-skip`: 3 (`TestPackageCommand/src_glob`, `TestIdleObjectsReaper`, `TestTensorflowServingEnv` — env-gated, t.Skip in CI without TS_RUNTIME_IMAGE)
- `deleted`: 0

## Tests

Columns:

- **Bash file** — path under `test/tests/`.
- **Phase** — current CI phase: `p1` (poolmgr/common, JOBS=6) or `p2` (newdeploy, JOBS=3) or `none` (not in `kind_CI.sh`).
- **Target suite** — `common`, `poolmgr`, or `newdeploy` under `test/integration/suites/`.
- **Go test** — proposed `Test<Name>` and file path. Filled in when migrated.
- **Status** — see legend.
- **PR** — link to migration PR. Filled in when migrated.

### Phase 1 (poolmgr / common, JOBS=6)

| Bash file | Phase | Target suite | Go test | Status | PR |
|-----------|-------|--------------|---------|--------|-----|
| `test_node_hello_http.sh` | p1 | common | `TestNodeHelloHTTP` (`common/node_hello_http_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_buildermgr.sh` | p1 | common | `TestBuilderMgr` (`common/buildermgr_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_canary.sh` | p1 | common | `TestCanary` (`common/canary_test.go`, subtests `success`+`rollback`) | bash-disabled-migrated / go-live | this PR |
| `test_pass.sh` | p1 | common | `TestPass` (`common/pass_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_annotations.sh` | p1 | common | `TestEnvironmentAnnotations` (`common/annotations_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_function_update.sh` | p1 | common | `TestFunctionUpdate` (`common/function_update_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_internal_routes.sh` | p1 | common | `TestInternalRoutes` (`common/internal_routes_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_logging/test_function_logs.sh` | p1 | common | `TestFunctionLogs` (`common/function_logs_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_huge_response/test_huge_response.sh` | p1 | common | `TestHugeResponse` (`common/huge_response_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_kubectl/test_kubectl.sh` | p1 | common | `TestKubectlApply` (`common/kubectl_test.go`) | bash-disabled-migrated / go-live | this PR |
| `websocket/test_ws.sh` | p1 | common | `TestWebsocket` (`common/websocket_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_archive_cli.sh` | p1 | common | `TestArchiveCLI` (`common/archive_cli_test.go`) | bash-active | — |
| `test_archive_pruner.sh` | p1 | common | `TestArchivePruner` (`common/archive_pruner_test.go`) | bash-active | — |
| `test_package_command.sh` | p1 | common | `TestPackageCommand` (`common/package_command_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_package_checksum.sh` | p1 | common | `TestPackageChecksum` (`common/package_checksum_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_specs/test_spec.sh` | p1 | common | `TestSpec` (`common/spec_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_specs/test_spec_multifile.sh` | p1 | common | `TestSpecMultifile` (`common/spec_multifile_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_specs/test_spec_merge/test_spec_merge.sh` | p1 | common | `TestSpecMerge` (`common/spec_merge_test.go`) | bash-active (deferred — pre-built yaml fixtures need templating) | — |
| `test_specs/test_spec_archive/test_spec_archive.sh` | p1 | common | `TestSpecArchive` (`common/spec_archive_test.go`) | bash-active (deferred — pre-built yaml fixtures need templating) | — |
| `test_env_podspec.sh` | p1 | common | `TestEnvPodSpec` (`common/env_podspec_test.go`) | bash-active | — |
| `test_environments/test_python_env.sh` | p1 | common | `TestPythonEnv` (`common/python_env_test.go`) | bash-active | — |
| `test_environments/test_go_env.sh` | p1 | common | `TestGoEnv` (`common/go_env_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_environments/test_tensorflow_serving_env.sh` | p1 | common | `TestTensorflowServingEnv` (`common/tensorflow_serving_env_test.go`) | bash-disabled-migrated / go-skip (env-gated) | this PR |
| `test_backend_poolmgr.sh` | p1 | common | `TestBackendPoolmgr` (`common/backend_poolmgr_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_fn_update/test_idle_objects_reaper.sh` | p1 | common | `TestIdleObjectsReaper` (`common/idle_objects_reaper_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_env_vars.sh` | p1 | common | TBD (Phase 5) | bash-disabled-existing | — |
| `test_function_test/test_fn_test.sh` | p1 | common | TBD (Phase 5) | bash-disabled-existing | — |
| `test_ingress.sh` | p1 | common | TBD (Phase 5) | bash-disabled-existing | — |

### Phase 2 (newdeploy, JOBS=3)

| Bash file | Phase | Target suite | Go test | Status | PR |
|-----------|-------|--------------|---------|--------|-----|
| `test_backend_newdeploy.sh` | p2 | common | `TestBackendNewdeploy` (`common/backend_newdeploy_test.go`, subtests `cold_start`+`warm_start`) | bash-disabled-migrated / go-live | this PR |
| `test_fn_update/test_scale_change.sh` | p2 | newdeploy | `TestScaleChange` (`newdeploy/scale_change_test.go`) | bash-active | — |
| `test_fn_update/test_configmap_update.sh` | p2 | common | `TestConfigMapUpdate` (`common/configmap_update_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_fn_update/test_env_update.sh` | p2 | newdeploy | `TestEnvUpdate` (`newdeploy/env_update_test.go`) | bash-active | — |
| `test_fn_update/test_resource_change.sh` | p2 | newdeploy | `TestResourceChange` (`newdeploy/resource_change_test.go`) | bash-active | — |
| `test_fn_update/test_secret_update.sh` | p2 | common | `TestSecretUpdate` (`common/secret_update_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_fn_update/test_nd_pkg_update.sh` | p2 | newdeploy | `TestNDPackageUpdate` (`newdeploy/nd_pkg_update_test.go`) | bash-active | — |
| `test_fn_update/test_poolmgr_nd.sh` | p2 | newdeploy | `TestPoolmgrToNewdeploy` (`newdeploy/poolmgr_nd_test.go`) | bash-active | — |
| `test_secret_cfgmap/test_secret_cfgmap.sh` | p2 | common | `TestSecretConfigMap` (`common/secret_cfgmap_test.go`, 7 subtests) | bash-disabled-migrated / go-live | this PR |
| `test_environments/test_nodejs_env.sh` | p2 | newdeploy | `TestNodejsEnv` (`newdeploy/nodejs_env_test.go`) | bash-active | — |
| `test_namespace/test_ns_current_context.sh` | p2 | common | `TestNamespaceCurrentContext` (`common/namespace_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_namespace/test_ns_flag.sh` | p2 | common | `TestNamespaceFlag` (`common/namespace_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_namespace/test_ns_env.sh` | p2 | common | `TestNamespaceEnv` (`common/ns_env_test.go`) | bash-active | — |
| `test_namespace/test_ns_deprecated_flag.sh` | p2 | common | `TestNamespaceDeprecatedFlag` (`common/namespace_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_obj_create_in_diff_ns.sh` | p2 | common | TBD (Phase 5) | bash-disabled-existing | — |
| `test_environments/test_java_builder.sh` | p2 | common | TBD (Phase 5) | bash-disabled-existing | — |
| `test_environments/test_java_env.sh` | p2 | common | TBD (Phase 5) | bash-disabled-existing | — |

### Not in current CI active list (still bash-runnable; migrate during bulk phase)

These three are not referenced by `kind_CI.sh` today, so they don't run in PR CI, but the files exist and are not `#test:disabled`. They get migrated during Phase 4 bulk batches alongside their thematic neighbors. After migration, the bash files are marked `#test:disabled` like everything else.

| Bash file | Target suite | Go test | Status | PR |
|-----------|--------------|---------|--------|-----|
| `test_create_fn_with_url.sh` | common | `TestCreateFunctionWithURL` (`common/create_fn_with_url_test.go`) | bash-disabled-migrated / go-live | this PR |
| `test_function_timeout.sh` | common | `TestFunctionTimeout` (`common/function_timeout_test.go`) | bash-active | — |
| `test_environments/test_jvm_jersey_env.sh` | common | `TestJvmJerseyEnv` (`common/jvm_jersey_env_test.go`) | bash-active (env-gated; CI skip when image unset) | — |

## Migration sequence

Tick off as PRs land. Update the table above each time.

### Phase 0 — Tracking docs (this PR)

- [x] Create `docs/test-migration/00-design.md`
- [x] Create `docs/test-migration/01-migration-status.md`
- [x] Create `docs/test-migration/02-framework-api.md`

### Phase 1 — Framework + Pilot 1 (this PR) ✅ green on K8s 1.34

- [x] `test/integration/framework/` — initial helper set
- [x] `test/integration/testdata/nodejs/hello/` — embedded fixture
- [x] `test/integration/suites/common/node_hello_http_test.go` — `TestNodeHelloHTTP` (PASS in 2.16s)
- [x] `.github/workflows/push_pr.yaml` — add Go test step + log artifact upload, port-forward inline
- [x] `test/tests/test_node_hello_http.sh` — `#test:disabled` + migration comment
- [x] `test/kind_CI.sh` — remove `test_node_hello_http.sh` from active list

Findings from the Phase 1 CI iteration that shaped subsequent design:
- Tests must use `default` namespace (router only watches `FISSION_RESOURCE_NAMESPACES`, default `default`).
- Background processes from a standalone port-forward step do not survive across GitHub Actions steps; port-forward lives inside each Go test step.

### Phase 2 — Pilot 2: builder (this PR)

- [x] `test/integration/framework/builder.go` — `WaitForBuilderReady`
- [x] `test/integration/framework/package.go` — `WaitForPackageBuildSucceeded`, `WaitForPackageBuildStatus`
- [x] `test/integration/framework/function.go` — `FunctionPackageName`; FunctionOptions extended with `Src`/`Entrypoint`/`BuildCmd`
- [x] `test/integration/framework/env.go` — EnvOptions extended with `Builder`
- [x] `test/integration/framework/testdata.go` — `ZipTestDataDir` (flat zip, mirrors `zip -j`)
- [x] `test/integration/framework/images.go` — `RequirePython`, `RequirePythonBuilder`
- [x] `test/integration/testdata/python/sourcepkg/` — embedded fixture
- [x] `test/integration/suites/common/buildermgr_test.go` — `TestBuilderMgr` with `rebuild_on_update` subtest
- [x] `.github/workflows/push_pr.yaml` — pre-pull `PYTHON_RUNTIME_IMAGE`/`PYTHON_BUILDER_IMAGE` in the Go test step
- [x] `test/tests/test_buildermgr.sh` — `#test:disabled` + migration comment
- [x] `test/kind_CI.sh` — remove `test_buildermgr.sh` from active list

### Phase 3 — Pilot 3: canary (this PR)

- [x] `test/integration/framework/canary.go` — `CreateCanaryConfig`
- [x] `test/integration/framework/httptrigger.go` — `FunctionWeight`, weighted `RouteOptions.FunctionWeights`, `WaitForFunctionWeight`
- [x] `test/integration/framework/env.go` — `EnvOptions.GracePeriod`
- [x] `test/integration/framework/router.go` — `RouterClient.FireRequests`
- [x] `test/integration/testdata/nodejs/hello_400/` — failing-function fixture
- [x] `test/integration/suites/common/canary_test.go` — `TestCanary/success` (weight ramps to 100% on healthy traffic) and `TestCanary/rollback` (weight returns to 0 after failure threshold)
- [x] Disable + remove bash counterpart

### Phase 4 — Bulk migration

PRs grouped by category. Each PR migrates 3–5 tests, marks the bash counterparts disabled, removes them from `kind_CI.sh`'s active list, and updates this file.

Suggested batches (ordered by approximate complexity):

1. **HTTP basics** ✅: `test_pass.sh`, `test_huge_response.sh`, `test_internal_routes.sh` migrated this PR. `test_annotations.sh` deferred — needs clientset Env construction (CLI doesn't expose `metadata.annotations`); migrate alongside other env-config tests in a later batch.
2. **Function ops** ✅: `test_function_update.sh`, `test_function_logs.sh`, `test_create_fn_with_url.sh`, `test_annotations.sh` migrated this PR.
3. **Specs** (partial ✅): `test_spec.sh`, `test_spec_multifile.sh` migrated this PR. `test_spec_merge.sh` and `test_spec_archive.sh` deferred — they ship pre-built yaml fixtures with hardcoded resource names (`nodep`, `nodend`, `dummyfoobarnode`, etc.) that would collide under `t.Parallel()`; need to template the yaml at test time before they can migrate.
4. **Archives & packages** (partial ✅): `test_package_command.sh`, `test_package_checksum.sh` migrated this PR. `test_archive_cli.sh` and `test_archive_pruner.sh` deferred — both depend on `fission archive` subcommands that stream to os.Stdout (which the in-process CLI helper can't capture under `t.Parallel()`); a clean migration likely needs direct storagesvc HTTP access from the test process.
5. **Environments** (partial ✅): `test_python_env.sh`, `test_nodejs_env.sh`, `test_env_podspec.sh`, `test_go_env.sh` migrated. `test_tensorflow_serving_env.sh` deferred — env-gated (TS_RUNTIME_IMAGE) and the bash builds the model archive at test time; needs a vendored `half_plus_two` model.
6. **Function updates** ✅: all 7 in `test_fn_update/` migrated; `TestIdleObjectsReaper` is t.Skip'd pending fsvc TTL investigation under parallel load.
7. **Backends** ✅: `test_backend_poolmgr.sh`, `test_backend_newdeploy.sh` migrated. `test_idle_objects_reaper.sh` migrated but t.Skip'd as above.
8. **Namespacing** (partial ✅): 3 of 4 migrated; `test_ns_env.sh` deferred — sets FISSION_DEFAULT_NAMESPACE, which conflicts with the framework's process-global ClientOptions.Namespace under t.Parallel.
9. **Misc** (partial ✅): `test_secret_cfgmap.sh` (7 subtests), `test_ws.sh` migrated. `test_kubectl.sh` deferred — needs `fission fn test` (not exposed via cobra writers) plus kubectl apply/replace semantics.

### Phase 5 — Disabled-test triage (PR pending)

Per-test decisions:

- `test_ingress.sh` — migrate; keep `t.Skip` when ingress controller not available (Kind default).
- `test_env_vars.sh` — investigate current state; migrate or delete based on findings.
- `test_obj_create_in_diff_ns.sh` — investigate cross-namespace CRD support; migrate or delete.
- `test_function_test/test_fn_test.sh` — investigate the in-development testing framework; migrate, skip, or delete.
- `test_environments/test_java_env.sh`, `test_java_builder.sh` — migrate behind `JVM_RUNTIME_IMAGE`/`JVM_BUILDER_IMAGE` env-gate; CI keeps skipping them.
- `test_environments/test_jvm_jersey_env.sh` — same env-gate as Java.

### Phase 6 — Bash teardown (PR pending)

- [ ] Delete `test/tests/*.sh` (all bash test scripts).
- [ ] Delete `test/run_test.sh`.
- [ ] Delete `test/utils.sh`, `test/test_utils.sh`, `test/init_tools.sh`.
- [ ] Delete or shrink `test/kind_CI.sh` (image preload may move into the workflow YAML).
- [ ] Remove `examples/` clone step from `.github/workflows/push_pr.yaml`.
- [ ] Decide: keep `docs/test-migration/02-framework-api.md` as permanent docs (move to `docs/integration-testing.md`?), delete the rest of the dir.
