// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rawZipEntry struct {
	name string
	mode os.FileMode
	body string
}

// writeRawZip writes a zip whose entry names and modes are set verbatim,
// bypassing any archive-time sanitization so that malicious names (the kind an
// attacker-supplied package can contain) reach the extractor unchanged.
func writeRawZip(t *testing.T, path string, entries ...rawZipEntry) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		hdr.SetMode(e.mode)
		w, err := zw.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = w.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}

// TestUnarchiveZipSlip verifies that a malicious archive entry cannot write
// outside the destination directory (zip-slip / CWE-22) and that symlink
// entries are refused, while benign archives still extract.
func TestUnarchiveZipSlip(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("parent traversal is refused", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		sentinel := filepath.Join(tmp, "sentinel")
		require.NoError(t, os.WriteFile(sentinel, []byte("intact"), 0o600))

		zipPath := filepath.Join(tmp, "evil.zip")
		writeRawZip(t, zipPath, rawZipEntry{name: "../escape.txt", mode: 0o644, body: "pwned"})

		err := Unarchive(ctx, zipPath, filepath.Join(tmp, "dst"))
		assert.Error(t, err)
		assert.NoFileExists(t, filepath.Join(tmp, "escape.txt"))

		got, err := os.ReadFile(sentinel)
		require.NoError(t, err)
		assert.Equal(t, "intact", string(got))
	})

	t.Run("absolute path is refused", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		abs := filepath.Join(tmp, "abs-escape.txt")
		zipPath := filepath.Join(tmp, "evil.zip")
		writeRawZip(t, zipPath, rawZipEntry{name: abs, mode: 0o644, body: "pwned"})

		err := Unarchive(ctx, zipPath, filepath.Join(tmp, "dst"))
		assert.Error(t, err)
		assert.NoFileExists(t, abs)
	})

	t.Run("symlink entry is refused", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		zipPath := filepath.Join(tmp, "evil.zip")
		writeRawZip(t, zipPath, rawZipEntry{name: "link", mode: 0o777 | os.ModeSymlink, body: "/etc/passwd"})

		dst := filepath.Join(tmp, "dst")
		err := Unarchive(ctx, zipPath, dst)
		assert.Error(t, err)
		_, lerr := os.Lstat(filepath.Join(dst, "link"))
		assert.True(t, os.IsNotExist(lerr), "no symlink should be created")
	})

	t.Run("benign archive still extracts", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		zipPath := filepath.Join(tmp, "ok.zip")
		writeRawZip(t, zipPath,
			rawZipEntry{name: "a.txt", mode: 0o644, body: "alpha"},
			rawZipEntry{name: "sub/b.txt", mode: 0o644, body: "beta"},
		)
		dst := filepath.Join(tmp, "dst")
		require.NoError(t, Unarchive(ctx, zipPath, dst))

		got, err := os.ReadFile(filepath.Join(dst, "a.txt"))
		require.NoError(t, err)
		assert.Equal(t, "alpha", string(got))
		got, err = os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
		require.NoError(t, err)
		assert.Equal(t, "beta", string(got))
	})
}

func TestIsZip(t *testing.T) {
	tests := []struct {
		name    string
		setupFn func() string
		want    bool
		wantErr bool
		cleanup bool
	}{
		{
			name: "valid zip file",
			setupFn: func() string {
				return "testdata/test.zip"
			},
			want:    true,
			wantErr: false,
			cleanup: false,
		},
		{
			name: "non-existent file",
			setupFn: func() string {
				return "testdata/non-existent.zip"
			},
			want:    false,
			wantErr: false,
			cleanup: true,
		},
		{
			name: "text file",
			setupFn: func() string {
				f, err := os.CreateTemp("", "test-*.txt")
				require.NoError(t, err)
				defer f.Close()
				if _, err := f.WriteString("hello world"); err != nil {
					t.Fatal(err)
				}
				return f.Name()
			},
			want:    false,
			wantErr: false,
			cleanup: true,
		},
		{
			name: "corrupt zip file",
			setupFn: func() string {
				f, err := os.CreateTemp("", "corrupt-*.zip")
				require.NoError(t, err)
				defer f.Close()
				if _, err := f.WriteString("corrupted content"); err != nil {
					t.Fatal(err)
				}
				return f.Name()
			},
			want:    true,
			wantErr: false,
			cleanup: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := tt.setupFn()
			if tt.cleanup && !filepath.IsAbs(filename) {
				// Cleanup only temp files
				t.Cleanup(func() {
					os.Remove(filename)
				})
			}

			got, err := IsZip(t.Context(), filename)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsZip() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsZip() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestArchiveUnarchive(t *testing.T) {
	ctx := t.Context()

	// Create temp test directories
	sourceDir, err := os.MkdirTemp("", "zip-test-source-*")
	require.NoError(t, err)
	defer os.RemoveAll(sourceDir)

	// Create test files and directories
	files := map[string][]byte{
		"file1.txt":           []byte("hello world"),
		"file2.txt":           []byte("test content"),
		"dir1/file3.txt":      []byte("nested file"),
		"dir1/dir2/file4.txt": []byte("deeply nested"),
	}

	for path, content := range files {
		fullPath := filepath.Join(sourceDir, path)
		err := os.MkdirAll(filepath.Dir(fullPath), 0755)
		require.NoError(t, err)
		err = os.WriteFile(fullPath, content, 0644)
		require.NoError(t, err)
	}

	// Create empty directory
	emptyDir := filepath.Join(sourceDir, "empty-dir")
	if err := os.Mkdir(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		srcPath string
		wantErr bool
	}{
		{
			name:    "archive and unarchive directory",
			srcPath: sourceDir,
			wantErr: false,
		},
		{
			name:    "archive and unarchive single file",
			srcPath: filepath.Join(sourceDir, "file1.txt"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp zip file
			zipFile, err := os.CreateTemp("", "test-*.zip")
			require.NoError(t, err)
			zipFile.Close()
			defer os.Remove(zipFile.Name())

			// Create temp extract directory
			extractDir, err := os.MkdirTemp("", "zip-test-extract-*")
			require.NoError(t, err)
			defer os.RemoveAll(extractDir)

			// Test Archive
			err = Archive(ctx, tt.srcPath, zipFile.Name())
			if (err != nil) != tt.wantErr {
				t.Errorf("Archive() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// Test is valid zip file
			isZip, err := IsZip(ctx, zipFile.Name())
			require.NoError(t, err)
			if !isZip {
				t.Errorf("Archive() did not create a valid zip file")
				return
			}

			// Test Unarchive
			err = Unarchive(ctx, zipFile.Name(), extractDir)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unarchive() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// Validate extracted content
			err = filepath.Walk(tt.srcPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}

				relPath, err := filepath.Rel(tt.srcPath, path)
				if err != nil {
					return err
				}
				if relPath == "." {
					return nil
				}

				extractedPath := filepath.Join(extractDir, relPath)

				extractedInfo, err := os.Stat(extractedPath)
				if err != nil {
					t.Errorf("Expected file %s not found in extracted directory", relPath)
					return nil
				}

				if info.Mode().Perm() != extractedInfo.Mode().Perm() {
					t.Errorf("File %s permissions mismatch: got %v, want %v",
						relPath, extractedInfo.Mode().Perm(), info.Mode().Perm())
				}

				if !info.IsDir() {
					originalContent, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					extractedContent, err := os.ReadFile(extractedPath)
					if err != nil {
						return err
					}
					if string(originalContent) != string(extractedContent) {
						t.Errorf("File %s content mismatch", relPath)
					}
				}
				return nil
			})
			require.NoError(t, err)
		})
	}
}

func TestArchiveOverwrite(t *testing.T) {
	ctx := t.Context()

	// Create initial source directory
	sourceDir, err := os.MkdirTemp("", "zip-test-source-*")
	require.NoError(t, err)
	defer os.RemoveAll(sourceDir)

	// Create initial files
	initialFiles := map[string][]byte{
		"old1.txt": []byte("old content 1"),
		"old2.txt": []byte("old content 2"),
	}
	for path, content := range initialFiles {
		fullPath := filepath.Join(sourceDir, path)
		if err := os.WriteFile(fullPath, content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create zip file
	zipFile, err := os.CreateTemp("", "test-*.zip")
	require.NoError(t, err)
	zipFile.Close()
	defer os.Remove(zipFile.Name())

	// Create initial zip
	if err := Archive(ctx, sourceDir, zipFile.Name()); err != nil {
		t.Fatal(err)
	}

	// Create new source directory with different content
	newSourceDir, err := os.MkdirTemp("", "zip-test-new-source-*")
	require.NoError(t, err)
	defer os.RemoveAll(newSourceDir)

	// Create new files
	newFiles := map[string][]byte{
		"new1.txt": []byte("new content 1"),
		"new2.txt": []byte("new content 2"),
	}
	for path, content := range newFiles {
		fullPath := filepath.Join(newSourceDir, path)
		if err := os.WriteFile(fullPath, content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Overwrite existing zip
	if err := Archive(ctx, newSourceDir, zipFile.Name()); err != nil {
		t.Fatal(err)
	}

	// Create extraction directory
	extractDir, err := os.MkdirTemp("", "zip-test-extract-*")
	require.NoError(t, err)
	defer os.RemoveAll(extractDir)

	// Extract overwritten zip
	if err := Unarchive(ctx, zipFile.Name(), extractDir); err != nil {
		t.Fatal(err)
	}

	// Validate extracted content
	files, err := os.ReadDir(extractDir)
	require.NoError(t, err)

	// Verify only new files exist
	expectedFiles := map[string]bool{
		"new1.txt": false,
		"new2.txt": false,
	}

	for _, f := range files {
		if _, ok := expectedFiles[f.Name()]; !ok {
			t.Errorf("Unexpected file found: %s", f.Name())
			continue
		}
		expectedFiles[f.Name()] = true

		// Verify content
		content, err := os.ReadFile(filepath.Join(extractDir, f.Name()))
		require.NoError(t, err)
		expected := newFiles[f.Name()]
		if string(content) != string(expected) {
			t.Errorf("File %s content mismatch: got %s, want %s",
				f.Name(), string(content), string(expected))
		}
	}

	// Verify old files do not exist
	oldFiles := []string{"old1.txt", "old2.txt"}
	for _, oldFile := range oldFiles {
		_, err := os.Stat(filepath.Join(extractDir, oldFile))
		if !os.IsNotExist(err) {
			t.Errorf("Old file %s should not exist in zip", oldFile)
		}
	}

	// Verify all expected files were found
	for name, found := range expectedFiles {
		if !found {
			t.Errorf("Expected file not found: %s", name)
		}
	}
}
