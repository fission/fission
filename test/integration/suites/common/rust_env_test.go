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

// TestRustEnv is the Go port of rust/tests/test_rust_env.sh.
//
// The Rust environment always builds through its builder image: even a
// single .rs file is compiled (the builder wraps it in the env's template
// crate), so unlike node/python there is no code-only path — both phases
// require RUST_RUNTIME_IMAGE and RUST_BUILDER_IMAGE.
//
// Two scenarios share one Environment + builder:
//
//  1. Single-file template: hello.rs exposes `pub async fn handler` that
//     returns "Hello, World!". Build it, wire up a poolmgr fn and a
//     newdeploy fn pointing at the same package, hit each via GET and
//     assert "Hello, World!".
//  2. Cargo project: zip project_example/ (Cargo.toml + src/main.rs, an
//     axum echo server). Build it, POST a JSON body, assert the echo.
//
// The first build of each package compiles the wrapper crate and the
// project build downloads crates (axum/tokio) from crates.io, so budget
// a generous ctx.
func TestRustEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireRust(t)
	builder := f.Images().RequireRustBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "rust-" + ns.ID
	fnPM := "fn-rust-pm-" + ns.ID
	fnND := "fn-rust-nd-" + ns.ID
	fnEcho := "fn-rust-echo-" + ns.ID

	// CreateEnv pre-waits for the builder pod + EndpointSlice when Builder
	// is set, so the immediate-next package build won't race the fetcher.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder, Period: 5,
	})

	// Phase 1 — single-file template. Rust functions have no entrypoint;
	// the env template always invokes `handler`.
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

	// Phase 2 — Cargo project (echo server). Zip Cargo.toml + src/ with no
	// top-level dir, mirroring `cd project-example && zip -r out Cargo.toml src`.
	pkgEcho := "rust-echo-" + ns.ID
	projZip := framework.ZipTestDataTree(t, "rust/project_example", "rust-project.zip")
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgEcho, Env: envName, Src: projZip,
	})
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgEcho)

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnEcho, Env: envName, Pkg: pkgEcho,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnEcho, URL: "/" + fnEcho, Method: "POST"})

	echoBody := f.Router(t).PostEventually(t, ctx, "/"+fnEcho, "application/json",
		[]byte(`{"hello":"rust"}`), framework.BodyContains("echo"))
	require.Contains(t, echoBody, "echo")
	require.Contains(t, echoBody, "rust")
}
