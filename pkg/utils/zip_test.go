package utils

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
				if err != nil {
					t.Fatal(err)
				}
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
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := f.WriteString("corrupted content"); err != nil {
					t.Fatal(err)
				}
				return f.Name()
			},
			want:    false,
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

			got, err := IsZip(context.Background(), filename)
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
	ctx := context.Background()

	// Create temp test directories
	sourceDir, err := os.MkdirTemp("", "zip-test-source-*")
	if err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(fullPath, content, 0644)
		if err != nil {
			t.Fatal(err)
		}
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
			if err != nil {
				t.Fatal(err)
			}
			zipFile.Close()
			defer os.Remove(zipFile.Name())

			// Create temp extract directory
			extractDir, err := os.MkdirTemp("", "zip-test-extract-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(extractDir)

			// Test Archive
			err = Archive(ctx, tt.srcPath, zipFile.Name())
			if (err != nil) != tt.wantErr {
				t.Errorf("Archive() error = %v, wantErr %v", err, tt.wantErr)
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
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArchiveOverwrite(t *testing.T) {
	ctx := context.Background()

	// Create initial source directory
	sourceDir, err := os.MkdirTemp("", "zip-test-source-*")
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	zipFile.Close()
	defer os.Remove(zipFile.Name())

	// Create initial zip
	if err := Archive(ctx, sourceDir, zipFile.Name()); err != nil {
		t.Fatal(err)
	}

	// Create new source directory with different content
	newSourceDir, err := os.MkdirTemp("", "zip-test-new-source-*")
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(extractDir)

	// Extract overwritten zip
	if err := Unarchive(ctx, zipFile.Name(), extractDir); err != nil {
		t.Fatal(err)
	}

	// Validate extracted content
	files, err := os.ReadDir(extractDir)
	if err != nil {
		t.Fatal(err)
	}

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
		if err != nil {
			t.Fatal(err)
		}
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
