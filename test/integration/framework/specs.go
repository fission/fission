//go:build integration

package framework

import (
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/testdata"
)

// MaterializeSpecs walks an embedded directory tree (e.g. a vendored
// `specs/` and any sibling source files), applies the replacements map
// to every file's contents (longest old-string first via strings.NewReplacer),
// and writes the result under workdir preserving the relative path from
// embedDir.
//
// Use this for spec-init/apply tests whose vendored YAMLs ship with
// hardcoded resource names that would collide under t.Parallel — replace
// them with TEST_ID-suffixed values before applying.
//
// Returns the materialized workdir for convenience (typically just t.TempDir()
// passed in unchanged).
func MaterializeSpecs(t *testing.T, embedDir string, replacements map[string]string, workdir string) string {
	t.Helper()
	r := newOrderedReplacer(replacements)
	require.NoErrorf(t, fs.WalkDir(testdata.FS, embedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := testdata.FS.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(embedDir, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(workdir, rel)
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
			return mkErr
		}
		out := r.Replace(string(b))
		return os.WriteFile(dst, []byte(out), 0o644)
	}), "MaterializeSpecs: walk embed %q", embedDir)
	return workdir
}

// newOrderedReplacer builds a strings.Replacer with longest-old-strings
// first so prefix overlaps (e.g. "nodep" vs "nodehellop") replace the
// longer match. strings.NewReplacer documents that argument order matters
// when patterns overlap; longest-first is the safe default for the kinds
// of name templating we do in spec migrations.
func newOrderedReplacer(m map[string]string) *strings.Replacer {
	pairs := make([]struct {
		old string
		new string
	}, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, struct {
			old string
			new string
		}{k, v})
	}
	// Sort longest-first.
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && len(pairs[j].old) > len(pairs[j-1].old); j-- {
			pairs[j], pairs[j-1] = pairs[j-1], pairs[j]
		}
	}
	flat := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		flat = append(flat, p.old, p.new)
	}
	return strings.NewReplacer(flat...)
}

// NewSpecUID returns an RFC-4122 v4 UUID for use as the spec
// DeploymentConfig.uid. Each test's spec apply gets a fresh UID so that
// `spec destroy` (label-selector by uid) deletes only that test's
// resources.
func NewSpecUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err, "NewSpecUID: rand")
	// RFC 4122 v4: set the version (4) and variant (10) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
