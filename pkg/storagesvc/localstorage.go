package storagesvc

import (
	"os"

	"github.com/graymeta/stow"
	_ "github.com/graymeta/stow/local"
	uuid "github.com/satori/go.uuid"
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
	id, err := uuid.NewV4()
	if err != nil {
		return "", err
	}
	return id.String(), err
}

func (ls localStorage) getSubDir() string {
	return ""
}

func (ls localStorage) getContainerName() string {
	return ls.containerName
}

func (ls localStorage) dial() (stow.Location, error) {
	cfg := stow.ConfigMap{"path": ls.localPath}
	return stow.Dial("local", cfg)
}

func (ls localStorage) getRegion() string {
	return ""
}
