// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// relUnderRoot converts a base-relative or absolute-under-base path into a name
// relative to base, rejecting absolute-escape and ".." traversal. It mirrors the
// os.Root relName contract used by pkg/storagesvc and is the validation behind
// the Root* helpers below.
func relUnderRoot(base, p string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("empty base directory")
	}
	name := filepath.FromSlash(p)
	if filepath.IsAbs(name) {
		rel, err := filepath.Rel(base, name)
		if err != nil {
			return "", fmt.Errorf("path %q is outside base %q", p, base)
		}
		name = rel
	}
	name = filepath.Clean(name)
	// ".." (and "../...") escape the base; reject them. "." (the base itself)
	// is inside the root and is allowed, matching the previous SanitizeFilePath
	// behavior for an empty/base-resolving path.
	if name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes base %q", p, base)
	}
	return name, nil
}

// RootJoin validates that name resolves to a location under base (no absolute
// escape, no ".." traversal) and returns the joined, cleaned path under base —
// absolute when base is absolute, as it always is for the callers here. It is
// the os.Root-style replacement for SanitizeFilePath: the result can be handed
// to downstream consumers unchanged.
func RootJoin(base, name string) (string, error) {
	rel, err := relUnderRoot(base, name)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, rel), nil
}

// RootStat stats path within base through an os.Root, so a traversing path
// cannot reach a file outside base. path may be base-relative or
// absolute-under-base.
func RootStat(base, path string) (os.FileInfo, error) {
	rel, err := relUnderRoot(base, path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(rel)
}

// RootWriteFile writes data to path within base through an os.Root.
func RootWriteFile(base, path string, data []byte, perm os.FileMode) error {
	rel, err := relUnderRoot(base, path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(rel, data, perm)
}

// RootMkdirAll creates path (and parents) within base through an os.Root. perm
// must be permission bits only (no os.ModeDir), which os.Root.MkdirAll requires.
func RootMkdirAll(base, path string, perm os.FileMode) error {
	rel, err := relUnderRoot(base, path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.MkdirAll(rel, perm.Perm())
}

// RootRename renames oldPath to newPath through an os.Root. Both ends must be
// under the same base.
func RootRename(base, oldPath, newPath string) error {
	relOld, err := relUnderRoot(base, oldPath)
	if err != nil {
		return err
	}
	relNew, err := relUnderRoot(base, newPath)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Rename(relOld, relNew)
}
