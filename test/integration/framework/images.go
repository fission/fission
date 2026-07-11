// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"os"

	storageclient "github.com/fission/fission/pkg/storagesvc/client"
)

// RuntimeImages holds the runtime/builder images used by tests, sourced
// from environment variables set in the "Go integration tests" step of
// .github/workflows/push_pr.yaml. Local runs need to export these
// manually; tests t.Skip when their required image is unset.
type RuntimeImages struct {
	Node             string
	NodeBuilder      string
	Python           string
	PythonBuilder    string
	Go               string
	GoBuilder        string
	JVM              string
	JVMBuilder       string
	JVMJersey        string
	JVMJerseyBuilder string
	Rust             string
	RustBuilder      string
	TS               string
	// Container is a plain HTTP-server image used by the container-executor
	// backend test (it serves HTTP itself, unlike Fission runtime images).
	Container string
}

func loadRuntimeImages() RuntimeImages {
	return RuntimeImages{
		Node:             os.Getenv("NODE_RUNTIME_IMAGE"),
		NodeBuilder:      os.Getenv("NODE_BUILDER_IMAGE"),
		Python:           os.Getenv("PYTHON_RUNTIME_IMAGE"),
		PythonBuilder:    os.Getenv("PYTHON_BUILDER_IMAGE"),
		Go:               os.Getenv("GO_RUNTIME_IMAGE"),
		GoBuilder:        os.Getenv("GO_BUILDER_IMAGE"),
		JVM:              os.Getenv("JVM_RUNTIME_IMAGE"),
		JVMBuilder:       os.Getenv("JVM_BUILDER_IMAGE"),
		JVMJersey:        os.Getenv("JVM_JERSEY_RUNTIME_IMAGE"),
		JVMJerseyBuilder: os.Getenv("JVM_JERSEY_BUILDER_IMAGE"),
		Rust:             os.Getenv("RUST_RUNTIME_IMAGE"),
		RustBuilder:      os.Getenv("RUST_BUILDER_IMAGE"),
		TS:               os.Getenv("TS_RUNTIME_IMAGE"),
		Container:        os.Getenv("CONTAINER_RUNTIME_IMAGE"),
	}
}

// RequireNode skips the test if NODE_RUNTIME_IMAGE is unset.
func (r RuntimeImages) RequireNode(skip skipper) string {
	return requireImage(skip, "NODE_RUNTIME_IMAGE", r.Node)
}

// RequirePython skips the test if PYTHON_RUNTIME_IMAGE is unset.
func (r RuntimeImages) RequirePython(skip skipper) string {
	return requireImage(skip, "PYTHON_RUNTIME_IMAGE", r.Python)
}

// RequirePythonBuilder skips the test if PYTHON_BUILDER_IMAGE is unset.
func (r RuntimeImages) RequirePythonBuilder(skip skipper) string {
	return requireImage(skip, "PYTHON_BUILDER_IMAGE", r.PythonBuilder)
}

// RequireGo skips the test if GO_RUNTIME_IMAGE is unset.
func (r RuntimeImages) RequireGo(skip skipper) string {
	return requireImage(skip, "GO_RUNTIME_IMAGE", r.Go)
}

// RequireGoBuilder skips the test if GO_BUILDER_IMAGE is unset.
func (r RuntimeImages) RequireGoBuilder(skip skipper) string {
	return requireImage(skip, "GO_BUILDER_IMAGE", r.GoBuilder)
}

// RequireTS skips the test if TS_RUNTIME_IMAGE (TensorFlow Serving) is unset.
func (r RuntimeImages) RequireTS(skip skipper) string {
	return requireImage(skip, "TS_RUNTIME_IMAGE", r.TS)
}

// RequireJVMJersey skips the test if JVM_JERSEY_RUNTIME_IMAGE is unset.
func (r RuntimeImages) RequireJVMJersey(skip skipper) string {
	return requireImage(skip, "JVM_JERSEY_RUNTIME_IMAGE", r.JVMJersey)
}

// RequireJVMJerseyBuilder skips the test if JVM_JERSEY_BUILDER_IMAGE is unset.
func (r RuntimeImages) RequireJVMJerseyBuilder(skip skipper) string {
	return requireImage(skip, "JVM_JERSEY_BUILDER_IMAGE", r.JVMJerseyBuilder)
}

// RequireRust skips the test if RUST_RUNTIME_IMAGE is unset.
func (r RuntimeImages) RequireRust(skip skipper) string {
	return requireImage(skip, "RUST_RUNTIME_IMAGE", r.Rust)
}

// RequireRustBuilder skips the test if RUST_BUILDER_IMAGE is unset.
func (r RuntimeImages) RequireRustBuilder(skip skipper) string {
	return requireImage(skip, "RUST_BUILDER_IMAGE", r.RustBuilder)
}

// RequireJVM skips the test if JVM_RUNTIME_IMAGE is unset.
func (r RuntimeImages) RequireJVM(skip skipper) string {
	return requireImage(skip, "JVM_RUNTIME_IMAGE", r.JVM)
}

// RequireJVMBuilder skips the test if JVM_BUILDER_IMAGE is unset.
func (r RuntimeImages) RequireJVMBuilder(skip skipper) string {
	return requireImage(skip, "JVM_BUILDER_IMAGE", r.JVMBuilder)
}

// RequireContainer skips the test if CONTAINER_RUNTIME_IMAGE is unset. It
// should point at a small image that serves HTTP on its port (the
// container-executor backend invokes the user image directly).
func (r RuntimeImages) RequireContainer(skip skipper) string {
	return requireImage(skip, "CONTAINER_RUNTIME_IMAGE", r.Container)
}

func requireImage(skip skipper, envVar, value string) string {
	if value == "" {
		skip.Skipf("%s is not set; skipping", envVar)
	}
	return value
}

// skipper is the subset of *testing.T used to skip a test when an env image is
// missing. Defined as an interface so the helper composes with t.Run subtests.
type skipper interface {
	Skipf(format string, args ...any)
}

// internalAuthSecretFromEnv returns the master HMAC key used to sign
// requests against the router internal listener. Empty when
// internalAuth is disabled in the cluster — the framework still issues
// requests, just without auth headers, and the verifier short-circuits
// to pass-through. Delegates to the shared storagesvc client helper so the
// env-var contract stays in one place.
func internalAuthSecretFromEnv() []byte {
	return storageclient.HMACSecretFromEnv()
}
