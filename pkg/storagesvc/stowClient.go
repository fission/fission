// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

type (
	// StorageType contains all different types of supported storage
	StorageType string

	storageConfig struct {
		storage Storage
	}

	// StowClient is the storage-service client. Historically it wrapped the
	// github.com/graymeta/stow abstraction (hence the name); it now drives an
	// internal objectStore backend (os-based local or minio-go/v7 S3) so the
	// fission-bundle binary no longer pulls in github.com/aws/aws-sdk-go v1.
	StowClient struct {
		logger  logr.Logger
		config  *storageConfig
		backend objectStore
	}
)

const (
	// StorageTypeLocal is a constant to hold local storate type name literal
	StorageTypeLocal StorageType = "local"
	// StorageTypeS3 is a constant to hold S3 storage type name literal
	StorageTypeS3 StorageType = "s3"
	// PaginationSize is a constant to hold no of pages
	PaginationSize = 10
)

var (
	ErrNotFound                = errors.New("not found")
	ErrRetrievingItem          = errors.New("unable to retrieve item")
	ErrOpeningItem             = errors.New("unable to open item")
	ErrWritingFile             = errors.New("unable to write file")
	ErrWritingFileIntoResponse = errors.New("unable to copy item into http response")
)

// MakeStowClient create a new StowClient for given storage
func MakeStowClient(logger logr.Logger, storage Storage) (*StowClient, error) {
	storageType := getStorageType(storage)
	if strings.Compare(storageType, "local") == 1 && strings.Compare(storageType, "s3") == 1 {
		return nil, errors.New("storage types other than 'local' and 's3' are not implemented")
	}

	config := &storageConfig{
		storage: storage,
	}

	backend, err := config.storage.dial()
	if err != nil {
		return nil, err
	}

	return &StowClient{
		logger:  logger.WithName("stow_client"),
		config:  config,
		backend: backend,
	}, nil
}

// putFile writes the file on the storage
func (client *StowClient) putFile(file multipart.File, fileSize int64) (string, error) {
	uploadName, err := client.config.storage.getUploadFileName()
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
func (client *StowClient) copyFileToStream(fileId string, w io.Writer) error {
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
func (client *StowClient) removeFileByID(itemID string) error {
	return client.backend.remove(itemID)
}

func (client *StowClient) getFileSize(itemID string) (int64, error) {
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
func (client *StowClient) exists(itemID string) error {
	ok, err := client.backend.exists(itemID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// filter defines an interface to filter out items from a set of items
type filter func(objectInfo, any) bool

// This method returns all items in a container, filtering out items based on the filter function passed to it
func (client *StowClient) getItemIDsWithFilter(filterFunc filter, filterFuncParam any) ([]string, error) {
	items, err := client.backend.list(client.config.storage.getSubDir())
	if err != nil {
		return nil, fmt.Errorf("error getting items from container: %w", err)
	}

	archiveIDList := make([]string, 0)
	for _, item := range items {
		if filterFunc(item, filterFuncParam) {
			continue
		}
		archiveIDList = append(archiveIDList, item.id)
	}

	return archiveIDList, nil
}

// filterItemCreatedAMinuteAgo is one type of filter function that filters out items created less than a minute ago.
// More filter functions can be written if needed, as long as they are of type filter
func (client StowClient) filterItemCreatedAMinuteAgo(item objectInfo, currentTime any) bool {
	if currentTime.(time.Time).Sub(item.lastMod) < 1*time.Minute {

		client.logger.V(1).Info("item created less than a minute ago",
			"item", item.id,
			"last_modified_time", item.lastMod)
		return true
	}
	return false
}

func (client StowClient) filterAllItems(item objectInfo, _ any) bool {
	client.logger.V(1).Info("item info",
		"item", item.id,
		"last_modified_time", item.lastMod)
	return false

}
