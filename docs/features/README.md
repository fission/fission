# Fork features

Features carried by this fork (`release/custom-v2.0`) on top of upstream
`fission/fission`. Each is re-implemented on upstream's controller-runtime
architecture (the pre-reconciler informer/watcher versions were removed upstream).

| Feature | Doc | Enables |
|---|---|---|
| Concurrent builder pods | [builder-concurrency.md](builder-concurrency.md) | demand-based builder pod pool, one pod per concurrent build |
| Builder scale-to-zero | [builder-scale-to-zero.md](builder-scale-to-zero.md) | idle builders scaled to 0, warmed back on demand |
| Newdeploy wait-for-build | [newdeploy-wait-for-build.md](newdeploy-wait-for-build.md) | newdeploy waits for the package build before provisioning |
| Watch all namespaces | [watch-all-namespaces.md](watch-all-namespaces.md) | watch Fission CRs cluster-wide without enumerating namespaces |

Design records for the two non-trivial ports live in `docs/spike-buildermgr-port.md`
and `docs/spike-watch-all-namespaces.md`.

## Configuration quick reference

| Setting | Where | Default |
|---|---|---|
| `spec.builder.poolsize` (`--builder-poolsize`) | Environment | 1 |
| `spec.builder.idleTimeout` (`--builder-idletimeout`) | Environment | 600s (0 = never) |
| `MAX_PARALLEL_BUILDS` | builder pod env | 1 |
| `BUILDERMGR_PACKAGE_CONCURRENCY` | buildermgr env | 5 |
| `BUILDER_IDLE_REAPER_INTERVAL` | buildermgr env | 10s |
| `NEWDEPLOY_BUILD_WAIT_TIMEOUT` | executor env | 600s |
| `watchAllNamespaces` | Helm value | true |
