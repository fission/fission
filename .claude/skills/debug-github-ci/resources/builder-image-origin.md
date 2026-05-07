# Env-builder images are NOT rebuilt per-PR

A subtle gotcha that has burned at least one CI debug cycle: when you change `pkg/builder/builder.go` and CI runs your PR, the env-builder pods (the ones running `python-builder`, `node-builder-22`, `go-builder-1.23`, etc.) **do not contain your changes**.

## Why

Fission has two ways binaries reach the cluster:

1. **Fission control-plane images** — built per-PR by `make skaffold-deploy`, which goreleaser-builds:
   - `fission-bundle` (multi-headed: hosts buildermgr, executor, router, storagesvc, kubewatcher, timer, mqtrigger, builder controller, canary, webhook, logger, pre-upgrade-checks dispatcher, ...)
   - `fetcher` (sidecar image)
   - `pre-upgrade-checks`
   - `reporter`
   - `builder` (the binary, used as a sidecar in the env-builder *pod*, but the env-builder *image* doesn't pull it from here)

2. **Environment images** — pre-built and published to GHCR by Fission environment maintainers, separately from this repo:
   - `ghcr.io/fission/python-builder`
   - `ghcr.io/fission/node-builder-22`
   - `ghcr.io/fission/go-builder-1.23`
   - … and one runtime image per env (e.g. `python-env`, `node-env-22`)

Each environment image is essentially `<lang base image> + /builder binary baked in + /build script`. The `/builder` binary inside that image is what runs in the builder container of the env-builder pod. **It was compiled at the time the image was built and pushed — not from this PR's source.**

## The cluster's view at PR test time

When CI integration tests run on PR:
- The fission-bundle, fetcher, etc. **do** reflect your PR's source code.
- The env-builder pods (e.g. `python-01e06b-6925-5d689969f5-6dmzg`) pull their image from `ghcr.io/fission/python-builder` and run the `/builder` binary that was baked into that image at release time.

So if you changed `pkg/builder/builder.go` to add a log line "builder received request" with new structured fields, you'll still see the **old** log format in the builder pod's logs in CI.

## How to verify which version is actually running

Read the structured logger output in the pod log and compare the `caller` field against your local file:

```
{"level":"info","msg":"builder received request","caller":"builder/builder.go:122","request":{"srcPkgFilename":"...","command":"./build.sh"}}
```

If `caller":"builder/builder.go:122"` points at a `return` or unrelated line in your local source, the binary in CI is from a different commit (the one baked into the env image).

## Workarounds for testing builder.go changes

1. **Unit + integration test mock** — write a Go test that exercises the changed code path without involving the env-builder image at all. This is the highest-confidence path.
2. **Build a local env-builder image** — there are Dockerfiles in the [`fission/environments` repo](https://github.com/fission/environments) (separate from this one). Build one locally with your patched `cmd/builder` binary, push to a registry, and override `Builder.Image` in the test's Environment CR. This is the path used when testing builder behaviour in a real cluster.
3. **Add a fission-bundle-only test** — the `fission-bundle` binary is rebuilt per-PR. Behaviour that lives in `pkg/buildermgr` (the controller, in fission-bundle) IS exercised. Behaviour purely in `cmd/builder` / `pkg/builder/builder.go` (the sidecar binary) is not.

## When *is* `pkg/builder/` exercised in CI?

The `pkg/builder` Go tests (`pkg/builder/builder_test.go`) run as part of the regular `make test-run` step. They cover the package logic in isolation. They do **not** run as part of the integration-test job — that one only exercises pre-built env-builder images.

## How to spot this confusion in logs

If you push a PR that changes `pkg/builder/builder.go` and the integration test still fails with behaviour matching the **old** code, but unit tests pass and lint is clean, this is almost certainly your situation. Check the `caller` line numbers in the builder pod log.
