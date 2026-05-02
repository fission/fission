//go:build integration

package framework

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestIDLabel matches the bash convention (clean_resource_by_id in test/utils.sh).
// Resources created by tests carry this label as a debugging aid; cleanup is
// driven by per-resource t.Cleanup hooks, not label selectors.
const TestIDLabel = "fission.io/test-id"

// TestNamespace is the per-test resource scope. It does not create a
// Kubernetes namespace — instead, all resources go into the well-known
// `default` namespace (the same namespace the bash tests use), and isolation
// between concurrent tests is provided by embedding TestNamespace.ID into
// every resource name.
//
// Why default? The deployed Fission router only watches namespaces in
// FISSION_RESOURCE_NAMESPACES (default: `default`), per
// pkg/utils/namespace.go. Creating Functions/HTTPTriggers in arbitrary
// namespaces would make them invisible to the router. Once Fission gains
// wildcard-namespace support, this can revert to one-namespace-per-test.
//
// TestNamespace is constructed via Framework.NewTestNamespace, which
// registers a single t.Cleanup that — in this exact order — (1) dumps
// diagnostics if t.Failed(), then (2) deletes every resource the test
// registered via Create* helpers. The single-cleanup model is required
// because t.Cleanup runs in LIFO; if each helper registered its own
// deletion via t.Cleanup, the diagnostics dump (registered first) would
// run *after* deletions and capture an empty namespace.
type TestNamespace struct {
	f        *Framework
	t        *testing.T
	Name     string // "default"
	ID       string // 6-hex character unique ID
	cleanups []namedCleanup
}

type namedCleanup struct {
	name string
	fn   func(context.Context) error
}

// addCleanup registers a per-resource cleanup. They run in reverse order of
// registration during the namespace cleanup, after the diagnostics dump.
func (ns *TestNamespace) addCleanup(name string, fn func(context.Context) error) {
	ns.cleanups = append(ns.cleanups, namedCleanup{name: name, fn: fn})
}

// NewTestNamespace returns a per-test scope rooted in the `default` namespace
// with a fresh ID. Tests should embed ns.ID into all resource names so
// concurrent tests don't collide.
//
// Registers a single t.Cleanup hook: dump diagnostics on failure, then run
// resource cleanups in LIFO order (skipped when TEST_NOCLEANUP=1).
func (f *Framework) NewTestNamespace(t *testing.T) *TestNamespace {
	t.Helper()
	ns := &TestNamespace{
		f:    f,
		t:    t,
		Name: metav1.NamespaceDefault,
		ID:   randomID(),
	}
	t.Cleanup(func() {
		if t.Failed() {
			ns.dumpDiagnostics()
		}
		if noCleanup() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		// LIFO so dependents (e.g. routes) are deleted before what they reference.
		for i := len(ns.cleanups) - 1; i >= 0; i-- {
			c := ns.cleanups[i]
			if err := c.fn(ctx); err != nil {
				t.Logf("cleanup %s: %v", c.name, err)
			}
		}
	})
	return ns
}

// noCleanup reports whether the test asked us to leave resources behind for
// post-mortem debugging.
func noCleanup() bool { return os.Getenv("TEST_NOCLEANUP") == "1" }

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "test"
	}
	return s
}

func randomID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
