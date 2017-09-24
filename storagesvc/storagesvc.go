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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/graymeta/stow"
	_ "github.com/graymeta/stow/local"
	"github.com/satori/go.uuid"
)

type (
	StorageType   string
	storageConfig struct {
		storageType   StorageType
		localPath     string
		containerName string
		// other stuff, such as google or s3 credentials, bucket names etc
	}

	StorageService struct {
		config    storageConfig
		location  stow.Location
		container stow.Container
		port      int
	}

	UploadResponse struct {
		ID string `json:"id"`
	}
)

const (
	StorageTypeLocal StorageType = "local"
)

// Handle multipart file uploads.
func (ss *StorageService) uploadHandler(w http.ResponseWriter, r *http.Request) {
	// handle upload
	r.ParseMultipartForm(0)
	file, handler, err := r.FormFile("uploadfile")
	if err != nil {
		http.Error(w, "missing upload file", 400)
		return
	}
	defer file.Close()

	// stow wants the file size, but that's different from the
	// content length, the content length being the size of the
	// encoded file in the HTTP request. So we require an
	// "X-File-Size" header in bytes.

	fileSizeS, ok := r.Header["X-File-Size"]
	if !ok {
		log.Printf("Missing X-File-Size")
		http.Error(w, "missing X-File-Size header", 400)
		return
	}

	fileSize, err := strconv.Atoi(fileSizeS[0])
	if err != nil {
		log.Printf("Error parsing x-file-size: '%v'", fileSizeS)
		http.Error(w, "missing or bad X-File-Size header", 400)
		return
	}

	// TODO: allow headers to add more metadata (e.g. environment
	// and function metadata)
	log.Printf("Handling upload for %v", handler.Filename)
	//fileMetadata := make(map[string]interface{})
	//fileMetadata["filename"] = handler.Filename

	// This is not the item ID (that's returned by Put)
	// should we just use handler.Filename? what are the constraints here?
	uploadName := uuid.NewV4().String()

	// save the file to the storage backend
	item, err := ss.container.Put(uploadName, file, int64(fileSize), nil)
	if err != nil {
		log.Printf("Error saving uploaded file: '%v'", err)
		http.Error(w, "Error saving uploaded file", 400)
		return
	}

	// respond with an ID that can be used to retrieve the file
	ur := &UploadResponse{
		ID: item.ID(),
	}
	resp, err := json.Marshal(ur)
	if err != nil {
		http.Error(w, "Error marshaling response", 500)
		return
	}
	w.Write(resp)
}

func (ss *StorageService) getIdFromRequest(r *http.Request) (string, error) {
	values := r.URL.Query()
	ids, ok := values["id"]
	if !ok || len(ids) == 0 {
		return "", errors.New("Missing `id' query param")
	}
	return ids[0], nil
}

func (ss *StorageService) deleteHandler(w http.ResponseWriter, r *http.Request) {
	// get id from request
	fileId, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	err = ss.container.RemoveItem(fileId)
	if err != nil {
		msg := fmt.Sprintf("Error deleting item: %v", err)
		http.Error(w, msg, 500)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (ss *StorageService) downloadHandler(w http.ResponseWriter, r *http.Request) {
	// get id from request
	fileId, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	// Get the file (called "item" in stow's jargon), open it,
	// stream it to response

	item, err := ss.container.Item(fileId)
	if err != nil {
		log.Printf("Error getting item id '%v': %v", fileId, err)
		if err == stow.ErrNotFound {
			http.Error(w, "Error retrieving item: not found", 404)
		} else {
			http.Error(w, "Error retrieving item", 400)
		}
		return
	}

	f, err := item.Open()
	if err != nil {
		log.Printf("Error opening item %v: %v", fileId, err)
		// TODO better http errors based on err
		http.Error(w, "Error opening item", 400)
		return
	}
	defer f.Close()

	_, err = io.Copy(w, f)
	if err != nil {
		log.Printf("Error writing response: %v", err)
		http.Error(w, "Error writing response", 500)
		return
	}
}

func MakeStorageService(sc *storageConfig) (*StorageService, error) {
	ss := &StorageService{
		config: *sc,
	}

	if sc.storageType != StorageTypeLocal {
		return nil, errors.New("Storage types other than 'local' are not implemented")
	}

	cfg := stow.ConfigMap{"path": sc.localPath}
	loc, err := stow.Dial("local", cfg)
	if err != nil {
		log.Printf("Error initializing storage: %v", err)
		return nil, err
	}
	ss.location = loc

	con, err := loc.CreateContainer(sc.containerName)
	if os.IsExist(err) {
		var cons []stow.Container
		var cursor string

		// use location.Containers to find containers that match the prefix (container name)
		cons, cursor, err = loc.Containers(sc.containerName, stow.CursorStart, 1)
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
	ss.container = con

	return ss, nil
}

func (ss *StorageService) Start(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/v1/archive", ss.uploadHandler).Methods("POST")
	r.HandleFunc("/v1/archive", ss.downloadHandler).Methods("GET")
	r.HandleFunc("/v1/archive", ss.deleteHandler).Methods("DELETE")

	address := fmt.Sprintf(":%v", port)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}

func RunStorageService(storageType StorageType, storagePath string, containerName string, port int) *StorageService {
	// storage
	ss, err := MakeStorageService(&storageConfig{
		storageType:   storageType,
		localPath:     storagePath,
		containerName: containerName,
	})
	if err != nil {
		log.Panicf("Error initializing storage: %v", err)
	}

	// http handlers
	go ss.Start(port)

	return ss
}
