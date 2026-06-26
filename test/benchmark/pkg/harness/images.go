// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package harness

import "os"

// Images holds the runtime/builder images a benchmark may use, resolved from the
// same environment variables the integration suite uses. An empty value means
// the image is unset; scenarios needing it skip themselves (see Require).
type Images struct {
	Python        string
	PythonBuilder string
	Node          string
	NodeBuilder   string
	Go            string
	GoBuilder     string
	JVM           string
	JVMBuilder    string
	Rust          string
	RustBuilder   string
	Container     string
}

// LoadImages reads the image environment variables.
func LoadImages() Images {
	return Images{
		Python:        os.Getenv("PYTHON_RUNTIME_IMAGE"),
		PythonBuilder: os.Getenv("PYTHON_BUILDER_IMAGE"),
		Node:          os.Getenv("NODE_RUNTIME_IMAGE"),
		NodeBuilder:   os.Getenv("NODE_BUILDER_IMAGE"),
		Go:            os.Getenv("GO_RUNTIME_IMAGE"),
		GoBuilder:     os.Getenv("GO_BUILDER_IMAGE"),
		JVM:           os.Getenv("JVM_RUNTIME_IMAGE"),
		JVMBuilder:    os.Getenv("JVM_BUILDER_IMAGE"),
		Rust:          os.Getenv("RUST_RUNTIME_IMAGE"),
		RustBuilder:   os.Getenv("RUST_BUILDER_IMAGE"),
		Container:     os.Getenv("CONTAINER_RUNTIME_IMAGE"),
	}
}
