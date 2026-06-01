// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// localObjectStore is an os-based objectStore rooted at <localPath>/<container>.
//
// To preserve backwards compatibility with the previous github.com/graymeta/stow
// "local" backend, object ids are the ABSOLUTE path of the stored file
// (filepath.Join(containerPath, name)). open/size/remove resolve an id directly
// as a path and list returns absolute paths, exactly as stow/local did.
type localObjectStore struct {
	// containerPath is the absolute path of <localPath>/<container>.
	containerPath string
}

// newLocalObjectStore creates (if necessary) the container directory under
// localPath and returns a localObjectStore rooted at it.
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

// resolve maps an id to a filesystem path. stow/local accepted both absolute
// ids (the form it produced) and container-relative ids; we mirror that.
func (s *localObjectStore) resolve(id string) string {
	if filepath.IsAbs(id) {
		return id
	}
	return filepath.Join(s.containerPath, filepath.FromSlash(id))
}

func (s *localObjectStore) put(name string, r io.Reader, size int64) (string, error) {
	path := filepath.Join(s.containerPath, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	// id is the absolute path, matching stow/local's item.ID().
	return path, nil
}

func (s *localObjectStore) open(id string) (io.ReadCloser, error) {
	f, err := os.Open(s.resolve(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *localObjectStore) size(id string) (int64, error) {
	info, err := os.Stat(s.resolve(id))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return info.Size(), nil
}

func (s *localObjectStore) remove(id string) error {
	err := os.Remove(s.resolve(id))
	if err != nil && os.IsNotExist(err) {
		return ErrNotFound
	}
	return err
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
		if prefix != "" && !hasPathPrefix(filepath.ToSlash(rel), prefix) {
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
	_, err := os.Stat(s.resolve(id))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// hasPathPrefix reports whether the slash-separated path p starts with prefix.
// It mirrors stow's string-prefix matching on relative names.
func hasPathPrefix(p, prefix string) bool {
	return len(p) >= len(prefix) && p[:len(prefix)] == prefix
}
