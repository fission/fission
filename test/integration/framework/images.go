//go:build integration

package framework

import "os"

// RuntimeImages holds the runtime/builder images used by tests, sourced from
// the same environment variables that test/kind_CI.sh sets in CI.
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
	TS               string
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
		TS:               os.Getenv("TS_RUNTIME_IMAGE"),
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

func routerURLFromEnv() string {
	if v := os.Getenv("FISSION_ROUTER"); v != "" {
		if hasScheme(v) {
			return v
		}
		return "http://" + v
	}
	return "http://127.0.0.1:8888"
}

func hasScheme(s string) bool {
	return len(s) > 7 && (s[:7] == "http://" || s[:8] == "https://")
}
