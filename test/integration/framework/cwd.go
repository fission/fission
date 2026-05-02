//go:build integration

package framework

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// cwdMutex serializes os.Chdir calls across concurrent tests. The Go process's
// cwd is global, but only a few CLI subcommands (`fission env create --spec`,
// `fn create --spec`, glob expansion in `--deploy`) actually depend on it —
// for those tests we chdir under this mutex; everything else runs unmodified.
var cwdMutex sync.Mutex

// WithCWD runs fn with the process working directory set to dir, with mutex
// serialization so two parallel tests don't race over the global cwd. The
// previous cwd is restored on exit.
//
// Used for spec-related tests: `fission env create --spec` and `fn create
// --spec` only support writing to `./specs` under the current cwd (no
// --specdir flag), and `fn create --deploy "<glob>"` expands the glob
// relative to cwd. Tests that need any of those CLI semantics wrap their
// calls in WithCWD.
//
// Concurrent non-spec tests are unaffected as long as they pass absolute
// file paths to the CLI (which all framework helpers do).
func (ns *TestNamespace) WithCWD(t *testing.T, dir string, fn func()) {
	t.Helper()
	cwdMutex.Lock()
	defer cwdMutex.Unlock()

	orig, err := os.Getwd()
	require.NoErrorf(t, err, "WithCWD: get current dir")
	require.NoErrorf(t, os.Chdir(dir), "WithCWD: chdir to %q", dir)
	defer func() {
		if cerr := os.Chdir(orig); cerr != nil {
			t.Logf("WithCWD: restore cwd %q: %v", orig, cerr)
		}
	}()
	fn()
}
