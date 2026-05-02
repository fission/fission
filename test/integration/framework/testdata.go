//go:build integration

package framework

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fission/fission/test/integration/testdata"
)

// WriteTestData materializes an embedded testdata file under t.TempDir() and
// returns the on-disk path. The CLI then reads from that path the same way
// the bash tests read from `examples/`.
//
// Example:
//
//	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
//	ns.CreateFunction(t, ctx, framework.FunctionOptions{Code: codePath, ...})
func WriteTestData(t *testing.T, embedPath string) string {
	t.Helper()
	b, err := testdata.FS.ReadFile(embedPath)
	if err != nil {
		t.Fatalf("WriteTestData: read embedded %q: %v", embedPath, err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, filepath.Base(embedPath))
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("WriteTestData: write %q: %v", dst, err)
	}
	return dst
}
