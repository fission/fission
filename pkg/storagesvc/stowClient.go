/*
Copyright 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package storagesvc

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"strings"
	"time"

	"errors"

	"github.com/graymeta/stow"
	"go.uber.org/zap"
)

type (
	// StorageType contains all different types of supported storage
	StorageType string

	storageConfig struct {
		storage Storage
	}

	// StowClient is the wrapper client for stow (Cloud storage abstraction package)
	StowClient struct {
		logger    *zap.Logger
		config    *storageConfig
		location  stow.Location
		container stow.Container
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

func getContainer(loc stow.Location, containerName string, cursor string) (stow.Container, error) {
	// use location.Containers to find containers that match the prefix (container name)
	cons, cursorNew, err := loc.Containers(containerName, cursor, 1)
	if err != nil {
		return nil, err
	}
	var con stow.Container
	for _, v := range cons {
		c, err := loc.Container(v.ID())
		if err != nil {
			return nil, err
		}
		if c.Name() == containerName {
			con = cons[0]
			break
		}
	}
	if con == nil && !stow.IsCursorEnd(cursorNew) {
		_, err := getContainer(loc, containerName, cursorNew)
		if err != nil {
			return nil, err
		}
	}
	return con, nil
}

func getOrCreateContainer(loc stow.Location, containerName string, cursor string) (stow.Container, error) {
	con, err := loc.CreateContainer(containerName)
	if err != nil && (os.IsExist(err) || strings.Contains(err.Error(), "BucketAlreadyOwnedByYou")) {
		con, err = getContainer(loc, containerName, stow.CursorStart)
	}
	if con == nil && err == nil {
		err = fmt.Errorf("storage container %s not found", containerName)
	}
	return con, err
}

// MakeStowClient create a new StowClient for given storage
func MakeStowClient(logger *zap.Logger, storage Storage) (*StowClient, error) {
	storageType := getStorageType(storage)
	if strings.Compare(storageType, "local") == 1 && strings.Compare(storageType, "s3") == 1 {
		return nil, errors.New("storage types other than 'local' and 's3' are not implemented")
	}

	config := &storageConfig{
		storage: storage,
	}

	stowClient := &StowClient{
		logger: logger.Named("stow_client"),
		config: config,
	}

	loc, err := getStorageLocation(config)
	if err != nil {
		return nil, err
	}
	stowClient.location = loc

	con, err := getOrCreateContainer(loc, config.storage.getContainerName(), stow.CursorStart)
	if err != nil {
		return nil, err
	}
	stowClient.container = con

	return stowClient, nil
}

// putFile writes the file on the storage
func (client *StowClient) putFile(file multipart.File, fileSize int64) (string, error) {
	uploadName, err := client.config.storage.getUploadFileName()
	if err != nil {
		return "", err
	}

	// save the file to the storage backend
	item, err := client.container.Put(uploadName, file, fileSize, nil)
	if err != nil {
		client.logger.Error("error writing file on storage",
			zap.Error(err),
			zap.String("file", uploadName))
		return "", ErrWritingFile
	}

	client.logger.Debug("successfully wrote file on storage", zap.String("file", uploadName))
	return item.ID(), nil
}

// copyFileToStream gets the file contents into a stream
func (client *StowClient) copyFileToStream(fileId string, w io.Writer) error {
	item, err := client.container.Item(fileId)
	if err != nil {
		if err == stow.ErrNotFound {
			return ErrNotFound
		} else {
			return ErrRetrievingItem
		}
	}

	f, err := item.Open()
	if err != nil {
		return ErrOpeningItem
	}
	defer f.Close()

	_, err = io.Copy(w, f)
	if err != nil {
		return ErrWritingFileIntoResponse
	}

	client.logger.Debug("successfully wrote file into httpresponse", zap.String("file", fileId))
	return nil
}

// removeFileByID deletes the file from storage
func (client *StowClient) removeFileByID(itemID string) error {
	return client.container.RemoveItem(itemID)
}

func (client *StowClient) getFileSize(itemID string) (int64, error) {
	item, err := client.container.Item(itemID)
	if err != nil {
		if err == stow.ErrNotFound {
			return 0, ErrNotFound
		} else {
			return 0, ErrRetrievingItem
		}
	}
	return item.Size()
}

// filter defines an interface to filter out items from a set of items
type filter func(stow.Item, interface{}) bool

// This method returns all items in a container, filtering out items based on the filter function passed to it
func (client *StowClient) getItemIDsWithFilter(filterFunc filter, filterFuncParam interface{}) ([]string, error) {
	cursor := stow.CursorStart
	var items []stow.Item
	var err error

	archiveIDList := make([]string, 0)

	for {
		items, cursor, err = client.container.Items(client.config.storage.getSubDir(), cursor, PaginationSize)
		if err != nil {
			return nil, fmt.Errorf("error getting items from container: %w", err)
		}

		for _, item := range items {
			isItemFilterable := filterFunc(item, filterFuncParam)
			if isItemFilterable {
				continue
			}
			archiveIDList = append(archiveIDList, item.ID())
		}

		if stow.IsCursorEnd(cursor) {
			break
		}
	}

	return archiveIDList, nil
}

// filterItemCreatedAMinuteAgo is one type of filter function that filters out items created less than a minute ago.
// More filter functions can be written if needed, as long as they are of type filter
func (client StowClient) filterItemCreatedAMinuteAgo(item stow.Item, currentTime interface{}) bool {
	itemLastModTime, _ := item.LastMod()
	if currentTime.(time.Time).Sub(itemLastModTime) < 1*time.Minute {

		client.logger.Debug("item created less than a minute ago",
			zap.String("item", item.ID()),
			zap.Time("last_modified_time", itemLastModTime))
		return true
	}
	return false
}

func (client StowClient) filterAllItems(item stow.Item, _ interface{}) bool {
	itemLastModTime, _ := item.LastMod()
	client.logger.Debug("item info",
		zap.String("item", item.ID()),
		zap.Time("last_modified_time", itemLastModTime))
	return false

}
