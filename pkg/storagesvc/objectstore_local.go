// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// localObjectStore is an os-based objectStore rooted at <localPath>/<container>.
//
// Every file operation goes through an os.Root opened on the container
// directory. Because the object id arrives from the request (?id=<id>), this
// matters for security: os.Root confines the operation to the container in the
// kernel, rejecting absolute paths and ".." traversal, so a crafted id can
// never read or delete a file outside it (e.g. ?id=/etc/passwd or ?id=../..).
// The root is opened per operation rather than held on the struct, so no
// directory file descriptor is kept for the process lifetime; the extra openat
// is negligible at storagesvc's request volume.
//
// Object ids are the absolute path of the stored file, the format the local
// backend has always produced; they are kept for in-place-upgrade compatibility
// and converted back to a root-relative name for each operation.
type localObjectStore struct {
	containerPath string
}

// newLocalObjectStore creates (if necessary) the container directory under
// localPath.
func newLocalObjectStore(localPath, container string) (*localObjectStore, error) {
	containerPath, err := filepath.Abs(filepath.Join(localPath, container))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(containerPath, 0o755); err != nil {
		return nil, err
	}
	return &localObjectStore{containerPath: containerPath}, nil
}

// relName converts an id to a name relative to the container root. ids are
// stored as absolute paths; os.Root operates on root-relative names. os.Root
// itself rejects anything that escapes the root, but we additionally reject
// absolute/".." escapes here so callers get a clean ErrNotFound rather than an
// opaque os.Root error.
func (s *localObjectStore) relName(id string) (string, error) {
	name := filepath.FromSlash(id)
	if filepath.IsAbs(name) {
		rel, err := filepath.Rel(s.containerPath, name)
		if err != nil {
			return "", ErrNotFound
		}
		name = rel
	}
	name = filepath.Clean(name)
	if name == "." || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
		return "", ErrNotFound
	}
	return name, nil
}

func (s *localObjectStore) put(name string, r io.Reader, _ int64) (string, error) {
	root, err := os.OpenRoot(s.containerPath)
	if err != nil {
		return "", err
	}
	defer root.Close()

	rel := filepath.FromSlash(name)
	// A namespace-scoped name (e.g. _tenant_/<ns>/<uuid>) nests under directories
	// that must exist before Create. MkdirAll runs through the os.Root, so it is
	// confined to the container — even a crafted name cannot create directories
	// outside it.
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}

	f, err := root.Create(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	// id is the absolute path of the stored file.
	return filepath.Join(s.containerPath, rel), nil
}

func (s *localObjectStore) open(id string) (io.ReadCloser, error) {
	name, err := s.relName(id)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.containerPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	f, err := root.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *localObjectStore) size(id string) (int64, error) {
	name, err := s.relName(id)
	if err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(s.containerPath)
	if err != nil {
		return 0, err
	}
	defer root.Close()

	info, err := root.Stat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return info.Size(), nil
}

func (s *localObjectStore) remove(id string) error {
	name, err := s.relName(id)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.containerPath)
	if err != nil {
		return err
	}
	defer root.Close()

	if err := root.Remove(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *localObjectStore) list(prefix string) ([]objectInfo, error) {
	infos := make([]objectInfo, 0)
	err := filepath.WalkDir(s.containerPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// prefix is relative to the container dir (empty for the local
		// backend, which uses no sub-directory).
		rel, relErr := filepath.Rel(s.containerPath, path)
		if relErr != nil {
			return relErr
		}
		if prefix != "" && !strings.HasPrefix(filepath.ToSlash(rel), prefix) {
			return nil
		}
		fi, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		infos = append(infos, objectInfo{id: path, lastMod: fi.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return infos, nil
}

func (s *localObjectStore) exists(id string) (bool, error) {
	name, err := s.relName(id)
	if err != nil {
		// An id that escapes the container (or is otherwise unresolvable) is
		// reported as absent rather than surfaced as an error.
		return false, nil
	}
	root, err := os.OpenRoot(s.containerPath)
	if err != nil {
		return false, err
	}
	defer root.Close()

	if _, err := root.Stat(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
