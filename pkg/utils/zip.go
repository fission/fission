package utils

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mholt/archives"
)

func IsZip(ctx context.Context, filename string) (bool, error) {
	f, err := os.Open(filename)
	if err != nil {
		return false, nil
	}
	result, err := archives.Zip{}.Match(ctx, filename, f)
	if err != nil {
		return false, err
	}
	if result.ByName || result.ByStream {
		return true, nil
	}
	return false, nil
}

func MakeZipArchiveWithGlobs(ctx context.Context, targetName string, globs ...string) (string, error) {
	globFiles, err := FindAllGlobs(globs...)
	if err != nil {
		return "", err
	}
	if len(globFiles) == 0 {
		return "", fmt.Errorf("no files found for globs: %v", globs)
	}
	files := make(map[string]string, len(globFiles))
	for _, file := range globFiles {
		files[file] = ""
	}

	archiveFiles, err := archives.FilesFromDisk(ctx, nil, files)
	if err != nil {
		return "", fmt.Errorf("failed to read files from disk: %w", err)
	}
	out, err := os.Create(targetName)
	if err != nil {
		return "", fmt.Errorf("failed to create archive file: %w", err)
	}
	defer out.Close()
	zip := archives.CompressedArchive{
		Archival: archives.Zip{},
	}
	if err := zip.Archive(ctx, out, archiveFiles); err != nil {
		return "", fmt.Errorf("failed to create archive: %w", err)
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

// Unarchive is a function that unzips a zip file to destination
func Unarchive(ctx context.Context, src string, dst string) error {
	var format archives.Zip
	file, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	return format.Extract(ctx, file, func(ctx context.Context, f archives.FileInfo) error {
		destPath := filepath.Join(dst, f.NameInArchive)
		// check if the file is a directory
		if f.IsDir() {
			return os.MkdirAll(destPath, f.Mode())
		}

		// check if parent directory exists for the file
		if err := os.MkdirAll(filepath.Dir(destPath), os.ModeDir|0755); err != nil {
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		// Open file in archive
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open file in archive: %w", err)
		}
		defer rc.Close()

		// Create file in destination
		destFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("failed to create file in destination: %w", err)
		}
		defer destFile.Close()
		err = destFile.Chmod(f.Mode())
		if err != nil {
			return fmt.Errorf("failed to set file permissions: %w", err)
		}

		// Copy file contents
		_, err = io.Copy(destFile, rc)
		if err != nil {
			return fmt.Errorf("failed to copy file contents: %w", err)
		}

		return nil
	})
}
