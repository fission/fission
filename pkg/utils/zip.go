// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mholt/archives"
)

// isZipStream sniffs whether the stream (named filename, used only for the
// extension heuristic) is a zip archive. It performs no filesystem access.
func isZipStream(ctx context.Context, filename string, r io.Reader) (bool, error) {
	result, err := archives.Zip{}.Match(ctx, filename, r)
	if err != nil {
		return false, err
	}
	return result.ByName || result.ByStream, nil
}

func IsZip(ctx context.Context, filename string) (bool, error) {
	f, err := os.Open(filename)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	return isZipStream(ctx, filename, f)
}

// IsZipInRoot is IsZip confined to base: filename is opened through an os.Root
// so an attacker-influenced path cannot escape base (CWE-22). Use this on the
// server-side fetch path where the path derives from request input.
func IsZipInRoot(ctx context.Context, base, filename string) (bool, error) {
	f, err := RootOpen(base, filename)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	return isZipStream(ctx, filename, f)
}

// writeZipArchive compresses the files matched by globs into out. It only reads
// the glob inputs and writes to the provided writer, so it performs no
// destination-path filesystem access of its own.
func writeZipArchive(ctx context.Context, out io.Writer, globs ...string) error {
	globFiles, err := FindAllGlobs(globs...)
	if err != nil {
		return err
	}
	if len(globFiles) == 0 {
		return fmt.Errorf("no files found for globs: %v", globs)
	}
	files := make(map[string]string, len(globFiles))
	for _, file := range globFiles {
		files[file] = ""
	}

	archiveFiles, err := archives.FilesFromDisk(ctx, nil, files)
	if err != nil {
		return fmt.Errorf("failed to read files from disk: %w", err)
	}
	zip := archives.CompressedArchive{
		Archival: archives.Zip{},
	}
	if err := zip.Archive(ctx, out, archiveFiles); err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}
	return nil
}

func MakeZipArchiveWithGlobs(ctx context.Context, targetName string, globs ...string) (string, error) {
	out, err := os.OpenFile(targetName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("failed to create archive file: %w", err)
	}
	defer out.Close()
	if err := writeZipArchive(ctx, out, globs...); err != nil {
		// Don't leave a partial/empty archive behind on failure, matching the
		// pre-refactor contract where the output was created only after the
		// glob inputs resolved successfully.
		os.Remove(targetName)
		return "", err
	}
	return filepath.Abs(targetName)
}

// Archive zips the contents of directory at src into a new zip file
// at dst (note that the contents are zipped, not the directory itself).
func Archive(ctx context.Context, src string, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to get source directory info: %w", err)
	}
	if srcInfo.IsDir() {
		src = src + "/*"
	}
	_, err = MakeZipArchiveWithGlobs(ctx, dst, src)
	return err
}

// ArchiveInRoot is Archive with src stat and dst creation confined to base
// through an os.Root, so request-derived paths cannot stat or write outside
// base (CWE-22). The glob expansion of src is unchanged from Archive.
func ArchiveInRoot(ctx context.Context, base, src, dst string) error {
	srcInfo, err := RootStat(base, src)
	if err != nil {
		return fmt.Errorf("failed to get source directory info: %w", err)
	}
	if srcInfo.IsDir() {
		src = src + "/*"
	}
	out, err := RootOpenFile(base, dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create archive file: %w", err)
	}
	err = writeZipArchive(ctx, out, src)
	out.Close()
	if err != nil {
		// Don't leave a partial/empty archive behind on failure, mirroring
		// MakeZipArchiveWithGlobs. dst is request-derived, so remove it through
		// the same os.Root rather than a bare os.Remove.
		_ = RootRemove(base, dst)
		return err
	}
	return nil
}

// Unarchive unzips the zip file at src into dst.
//
// Extraction is confined to dst through an os.Root: the archive entry name
// arrives from a user-supplied package, so a crafted name (e.g. "../../etc/x"
// or an absolute path) must not write outside dst. os.Root enforces that in the
// kernel; we also reject escaping names and symlink entries up front for a
// clear error (zip-slip / CWE-22).
func Unarchive(ctx context.Context, src string, dst string) error {
	file, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()
	return unarchiveStream(ctx, file, dst)
}

// UnarchiveInRoot is Unarchive with the source archive opened through an
// os.Root rooted at base, so a request-derived src path cannot read a file
// outside base (CWE-22). Extraction into dst is confined exactly as in
// Unarchive.
func UnarchiveInRoot(ctx context.Context, base, src, dst string) error {
	file, err := RootOpen(base, src)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()
	return unarchiveStream(ctx, file, dst)
}

// unarchiveStream extracts the zip stream file into dst. Extraction is confined
// to dst through an os.Root (see Unarchive's doc comment for the threat model).
func unarchiveStream(ctx context.Context, file io.Reader, dst string) error {
	var format archives.Zip

	// The destination must exist before we can open a root on it. Mode 0o755 is
	// required so that other containers in the same pod (running under
	// different UIDs / GIDs — fetcher sidecar at UID 10001 vs. builder
	// running as root) can read this shared /packages volume. Tighter
	// modes break cross-container access for the v2 builder flow.
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("failed to open destination root: %w", err)
	}
	defer root.Close()

	return format.Extract(ctx, file, func(ctx context.Context, f archives.FileInfo) error {
		// Confine the archive entry to dst.
		name := filepath.Clean(filepath.FromSlash(f.NameInArchive))
		if name == "." {
			return nil // archive root; dst already exists
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q escapes destination", f.NameInArchive)
		}
		// Refuse symlink entries: function packages have no need for them, and
		// they are an avenue for escaping the extraction root.
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q is a symlink; refusing to extract", f.NameInArchive)
		}

		// check if the file is a directory
		if f.IsDir() {
			return root.MkdirAll(name, f.Mode().Perm())
		}

		// Create the parent directory at 0o755 for the same cross-container
		// access reason as the destination root above.
		if dir := filepath.Dir(name); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}
		}

		// Open file in archive
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open file in archive: %w", err)
		}
		defer rc.Close()

		// Create file in destination with the archive entry's mode applied
		// at create time, so a concurrent observer never sees a wider mode.
		// Same overwrite semantics as os.Create.
		destFile, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, f.Mode().Perm())
		if err != nil {
			return fmt.Errorf("failed to create file in destination: %w", err)
		}
		defer destFile.Close()

		// Copy file contents
		_, err = io.Copy(destFile, rc)
		if err != nil {
			return fmt.Errorf("failed to copy file contents: %w", err)
		}

		return nil
	})
}
