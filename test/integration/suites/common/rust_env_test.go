// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestRustEnv is the Go port of rust/tests/test_rust_env.sh (single-file path).
//
// The Rust environment always builds through its builder image: even a single
// .rs file is compiled (the builder wraps it in the env's template crate), so
// unlike node/python there is no code-only path — the test requires both
// RUST_RUNTIME_IMAGE and RUST_BUILDER_IMAGE.
//
// hello.rs exposes `pub async fn handler` returning "Hello, World!". We build
// it, wire up a poolmgr fn and a newdeploy fn pointing at the same package, hit
// each via GET and assert "Hello, World!".
//
// Note: the bash suite also exercises a full Cargo project (echo server). We
// intentionally don't port that — it pulls crates (axum/tokio) from crates.io
// at build time, and the CI build pods have no reliable internet egress (the
// same reason TestGoEnv's module phase is flaky). The single-file path reuses
// crates already cached in the builder image, so it builds offline.
func TestRustEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireRust(t)
	builder := f.Images().RequireRustBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "rust-" + ns.ID
	fnPM := "fn-rust-pm-" + ns.ID
	fnND := "fn-rust-nd-" + ns.ID

	// CreateEnv pre-waits for the builder pod + EndpointSlice when Builder
	// is set, so the immediate-next package build won't race the fetcher.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder, Period: 5,
	})

	// Single-file template. Rust functions have no entrypoint; the env
	// template always invokes `handler`.
	pkgHello := "rust-hello-" + ns.ID
	helloPath := framework.WriteTestData(t, "rust/hello_world/hello.rs")
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgHello, Env: envName, Src: helloPath,
	})
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgHello)

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnPM, Env: envName, Pkg: pkgHello,
	})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnND, Env: envName, Pkg: pkgHello, ExecutorType: "newdeploy",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnPM, URL: "/" + fnPM, Method: "GET"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnND, URL: "/" + fnND, Method: "GET"})

	bodyPM := f.Router(t).GetEventually(t, ctx, "/"+fnPM, framework.BodyContains("Hello, World!"))
	require.Contains(t, bodyPM, "Hello, World!")
	bodyND := f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("Hello, World!"))
	require.Contains(t, bodyND, "Hello, World!")
}
