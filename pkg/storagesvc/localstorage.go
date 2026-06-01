// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"os"

	"github.com/fission/fission/pkg/utils/uuid"
)

type localStorage struct {
	storageType   StorageType
	containerName string
	localPath     string
}

// NewLocalStorage return new local storage struct
func NewLocalStorage(localPath string) Storage {
	subdir := os.Getenv("SUBDIR")
	if len(subdir) == 0 {
		subdir = "fission-functions"
	}
	return localStorage{
		storageType:   StorageTypeLocal,
		containerName: subdir,
		localPath:     localPath,
	}
}

// Local
func (ls localStorage) getStorageType() StorageType {
	return ls.storageType
}

func (ls localStorage) getUploadFileName() (string, error) {
	// This is not the item ID (that's returned by Put)
	// should we just use handler.Filename? what are the constraints here?
	return uuid.NewString(), nil
}

func (ls localStorage) getSubDir() string {
	return ""
}

func (ls localStorage) getContainerName() string {
	return ls.containerName
}

func (ls localStorage) dial() (objectStore, error) {
	return newLocalObjectStore(ls.localPath, ls.containerName)
}
