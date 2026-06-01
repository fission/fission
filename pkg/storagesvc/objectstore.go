// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"io"
	"time"
)

// objectInfo is the minimal metadata the storage service needs about a stored
// object: its opaque id and the time it was last modified.
type objectInfo struct {
	// id is the opaque identifier stored in Package URLs as ?id=<id>.
	// For the local backend it is the absolute path of the stored file; for
	// the s3 backend it is the object key (path.Join(subDir, uuid)). Both
	// formats are preserved exactly as the previous graymeta/stow-backed
	// implementation produced them, so archives created before an in-place
	// upgrade keep resolving.
	id string
	// lastMod is the object's last-modified time, used by the archive pruner
	// to skip recently created (not-yet-referenced) archives.
	lastMod time.Time
}

// objectStore is an internal abstraction over the two supported storage
// backends (local filesystem and S3). It replaces the previous
// github.com/graymeta/stow Location/Container pair so that the S3 path can use
// github.com/minio/minio-go/v7 directly and the binary no longer pulls in the
// github.com/aws/aws-sdk-go v1 endpoint map.
type objectStore interface {
	// put stores the object under name (relative to the backend's container)
	// and returns its opaque id.
	put(name string, r io.Reader, size int64) (id string, err error)
	// open returns a reader for the object with the given id. It returns
	// ErrNotFound if the object does not exist.
	open(id string) (io.ReadCloser, error)
	// size returns the byte size of the object with the given id. It returns
	// ErrNotFound if the object does not exist.
	size(id string) (int64, error)
	// remove deletes the object with the given id.
	remove(id string) error
	// list returns metadata for every object under prefix.
	list(prefix string) ([]objectInfo, error)
	// exists reports whether an object with the given id is present.
	exists(id string) (bool, error)
}
