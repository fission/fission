package storagesvc

import (
	"log"
	"io"
	"mime/multipart"
	"github.com/satori/go.uuid"
	"github.com/graymeta/stow"
	_ "github.com/graymeta/stow/local"
	"errors"
	"os"
	"net/http"
)

type (
	StorageType   string

	storageConfig struct {
		storageType   StorageType
		localPath     string
		containerName string
		// other stuff, such as google or s3 credentials, bucket names etc
	}

	StowClient struct {
		config    *storageConfig
		location  stow.Location
		container stow.Container
	}
)

const (
	StorageTypeLocal StorageType = "local"
)

var (
	ErrNotFound = errors.New("not found")
	ErrRetrievingItem = errors.New("not able to retrieve file")
	ErrOpeningItem = errors.New("not able to open item")
	ErrWritingFile = errors.New("not able to write file")
	ErrWritingFileIntoResponse = errors.New("not able to copy file into http response")
)

func MakeStowClient(storageType StorageType, storagePath string, containerName string) (*StowClient, error) {
	if storageType != StorageTypeLocal {
		return nil, errors.New("Storage types other than 'local' are not implemented")
	}

	config := &storageConfig{
		storageType:   storageType,
		localPath:     storagePath,
		containerName: containerName,
	}

	stowClient := &StowClient{
		config: config,
	}


	cfg := stow.ConfigMap{"path": config.localPath}
	loc, err := stow.Dial("local", cfg)
	if err != nil {
		log.Printf("Error initializing storage: %v", err)
		return nil, err
	}
	stowClient.location = loc

	con, err := loc.CreateContainer(config.containerName)
	if os.IsExist(err) {
		var cons []stow.Container
		var cursor string

		// use location.Containers to find containers that match the prefix (container name)
		cons, cursor, err = loc.Containers(config.containerName, stow.CursorStart, 1)
		if err == nil {
			if !stow.IsCursorEnd(cursor) {
				// Should only have one storage container
				err = errors.New("Found more than one matched storage containers")
			} else {
				con = cons[0]
			}
		}
	}
	if err != nil {
		log.Printf("Error initializing storage: %v", err)
		return nil, err
	}
	stowClient.container = con

	return stowClient

}


func (client *StowClient) putFile(file multipart.File, fileSize int64) (string, error) {
	// This is not the item ID (that's returned by Put)
	// should we just use handler.Filename? what are the constraints here?
	uploadName := uuid.NewV4().String()

	// save the file to the storage backend
	item, err := client.container.Put(uploadName, file, int64(fileSize), nil)
	if err != nil {
		log.Printf("Error writing file:%s on storage: '%v'", uploadName, err)
		return "", ErrWritingFile
	}

	log.Printf("Successfully wrote file:%s on storage", uploadName)
	return item.ID(), nil
}

func (client *StowClient) getFileIntoResponseWriter(fileId string, w *http.ResponseWriter) error {
	item, err := client.container.Item(fileId)
	if err != nil {
		log.Printf("Error getting item id '%v': %v", fileId, err)
		if err == stow.ErrNotFound {
			return ErrNotFound
		} else {
			return ErrRetrievingItem
		}
	}

	f, err := item.Open()
	if err != nil {
		log.Printf("Error opening item %v: %v", fileId, err)
		return ErrOpeningItem
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	if err != nil {
		log.Printf("Error writing item %v: %v to http response", fileId, err)
		return ErrWritingFileIntoResponse
	}

	return nil
}

func (client *StowClient) removeFileByID(itemID string) error {
	return client.container.RemoveItem(itemID)
}
