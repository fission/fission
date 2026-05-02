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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestIDLabel matches the bash convention (clean_resource_by_id in test/utils.sh)
// so legacy tooling can still find Go-test-created resources if needed.
const TestIDLabel = "fission.io/test-id"

// TestNamespace is a per-test cluster namespace plus convenience helpers
// (CLI invocation, resource creation, diagnostic dumps). It is constructed via
// Framework.NewTestNamespace and registers its own cleanup.
type TestNamespace struct {
	f    *Framework
	t    *testing.T
	Name string
	ID   string
}

// Framework returns the parent framework.
func (ns *TestNamespace) Framework() *Framework { return ns.f }

// NewTestNamespace creates `fission-it-<sanitized-test-name>-<rand>`, labels
// it `fission.io/test-id=<id>`, and registers cleanup. If TEST_NOCLEANUP=1 is
// set, the namespace is left in place for debugging.
func (f *Framework) NewTestNamespace(t *testing.T) *TestNamespace {
	t.Helper()
	id := randomID()
	name := nsName(t.Name(), id)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := f.kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				TestIDLabel:                        id,
				"fission.io/integration-test":      "true",
				"fission.io/integration-test-name": sanitize(t.Name()),
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create test namespace %q: %v", name, err)
	}

	ns := &TestNamespace{f: f, t: t, Name: name, ID: id}

	t.Cleanup(func() {
		if t.Failed() {
			ns.dumpDiagnostics()
		}
		if os.Getenv("TEST_NOCLEANUP") == "1" {
			t.Logf("TEST_NOCLEANUP=1 set; leaving namespace %s for inspection", name)
			return
		}
		delCtx, delCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer delCancel()
		err := f.kubeClient.CoreV1().Namespaces().Delete(delCtx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("delete test namespace %q: %v", name, err)
		}
	})

	return ns
}

// nsName builds a DNS-1123-compliant namespace name within the 63-char limit
// (`fission-it-` prefix is 11 chars, `-<6char-id>` suffix is 7 chars,
// leaving 45 chars for the sanitized test name).
func nsName(testName, id string) string {
	const prefix = "fission-it-"
	const maxNameLen = 63 - len(prefix) - 1 - 6 // 45
	s := sanitize(testName)
	if len(s) > maxNameLen {
		s = s[:maxNameLen]
	}
	s = strings.Trim(s, "-")
	if s == "" {
		s = "test"
	}
	return prefix + s + "-" + id
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	return s
}

func randomID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
