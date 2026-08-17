// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"time"

	"github.com/go-logr/logr"
)

type (
	// StorageType contains all different types of supported storage
	StorageType string

	storageConfig struct {
		storage Storage
	}

	// StorageClient is the storage-service client. It drives an internal
	// objectStore backend (os-based local or minio-go/v7 S3).
	StorageClient struct {
		logger  logr.Logger
		config  *storageConfig
		backend objectStore
	}
)

const (
	// StorageTypeLocal is a constant to hold local storage type name literal
	StorageTypeLocal StorageType = "local"
	// StorageTypeS3 is a constant to hold S3 storage type name literal
	StorageTypeS3 StorageType = "s3"
	// PaginationSize is a constant to hold no of pages
	PaginationSize = 10
)

var (
	ErrNotFound                = errors.New("not found")
	ErrRetrievingItem          = errors.New("unable to retrieve item")
	ErrWritingFile             = errors.New("unable to write file")
	ErrWritingFileIntoResponse = errors.New("unable to copy item into http response")
)

// MakeStorageClient create a new StorageClient for given storage
func MakeStorageClient(logger logr.Logger, storage Storage) (*StorageClient, error) {
	storageType := getStorageType(storage)
	if storageType != string(StorageTypeLocal) && storageType != string(StorageTypeS3) {
		return nil, errors.New("storage types other than 'local' and 's3' are not implemented")
	}

	config := &storageConfig{
		storage: storage,
	}

	backend, err := config.storage.dial()
	if err != nil {
		return nil, err
	}

	return &StorageClient{
		logger:  logger.WithName("storage_client"),
		config:  config,
		backend: backend,
	}, nil
}

// putFile writes the file on the storage. namespace, when non-empty, scopes the
// generated id to that tenant (see getUploadFileName / authz.go); empty yields a
// legacy unscoped id.
func (client *StorageClient) putFile(file multipart.File, fileSize int64, namespace string) (string, error) {
	uploadName, err := client.config.storage.getUploadFileName(namespace)
	if err != nil {
		return "", err
	}

	// save the file to the storage backend
	id, err := client.backend.put(uploadName, file, fileSize)
	if err != nil {
		client.logger.Error(err, "error writing file on storage", "file", uploadName)
		return "", ErrWritingFile
	}

	client.logger.V(1).Info("successfully wrote file on storage", "file", uploadName)
	return id, nil
}

// copyFileToStream gets the file contents into a stream
func (client *StorageClient) copyFileToStream(fileId string, w io.Writer) error {
	f, err := client.backend.open(fileId)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		// open() locates and opens the object in one step; a non-NotFound
		// failure is a retrieval error, matching the previous Item() probe.
		return ErrRetrievingItem
	}
	defer f.Close()

	_, err = io.Copy(w, f)
	if err != nil {
		return ErrWritingFileIntoResponse
	}

	client.logger.V(1).Info("successfully wrote file into httpresponse", "file", fileId)
	return nil
}

// removeFileByID deletes the file from storage
func (client *StorageClient) removeFileByID(itemID string) error {
	return client.backend.remove(itemID)
}

func (client *StorageClient) getFileSize(itemID string) (int64, error) {
	size, err := client.backend.size(itemID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, ErrNotFound
		}
		return 0, ErrRetrievingItem
	}
	return size, nil
}

// exists reports whether an archive with the given id is present in storage,
// returning ErrNotFound if it is not.
func (client *StorageClient) exists(itemID string) error {
	ok, err := client.backend.exists(itemID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// filter reports whether an item must be left out of a listing.
type filter func(objectInfo) bool

// getItemIDsWithFilter returns the IDs of all items in the container that the
// filter does not exclude.
func (client *StorageClient) getItemIDsWithFilter(exclude filter) ([]string, error) {
	items, err := client.backend.list(client.config.storage.getSubDir())
	if err != nil {
		return nil, fmt.Errorf("error getting items from container: %w", err)
	}

	archiveIDList := make([]string, 0)
	for _, item := range items {
		if exclude(item) {
			continue
		}
		archiveIDList = append(archiveIDList, item.id)
	}

	return archiveIDList, nil
}

// filterItemsNewerThan returns a filter that excludes items modified less than
// minAge before now. The archive pruner uses it so an archive that a build is
// still uploading is never treated as orphaned.
func (client StorageClient) filterItemsNewerThan(now time.Time, minAge time.Duration) filter {
	return func(item objectInfo) bool {
		if now.Sub(item.lastMod) < minAge {
			client.logger.V(1).Info("item modified too recently to prune",
				"item", item.id,
				"last_modified_time", item.lastMod,
				"min_age", minAge)
			return true
		}
		return false
	}
}

// filterAllItems excludes nothing; it only logs each item at debug level.
func (client StorageClient) filterAllItems(item objectInfo) bool {
	client.logger.V(1).Info("item info",
		"item", item.id,
		"last_modified_time", item.lastMod)
	return false
}
