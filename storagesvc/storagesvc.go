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
	log "github.com/sirupsen/logrus"
	"net/http"
	"os"
	"strconv"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	_ "github.com/graymeta/stow/local"
)

type (
	StorageService struct {
		storageClient *StowClient
		port          int
	}

	UploadResponse struct {
		ID string `json:"id"`
	}
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
		log.Error("Missing X-File-Size")
		http.Error(w, "missing X-File-Size header", 400)
		return
	}

	fileSize, err := strconv.Atoi(fileSizeS[0])
	if err != nil {
		log.WithError(err).Errorf("Error parsing x-file-size: '%v'", fileSizeS)
		http.Error(w, "missing or bad X-File-Size header", 400)
		return
	}

	// TODO: allow headers to add more metadata (e.g. environment
	// and function metadata)
	log.Infof("Handling upload for %v", handler.Filename)
	//fileMetadata := make(map[string]interface{})
	//fileMetadata["filename"] = handler.Filename

	id, err := ss.storageClient.putFile(file, int64(fileSize))
	if err != nil {
		log.WithError(err).Error("Error saving uploaded file")
		http.Error(w, "Error saving uploaded file", 500) // TODO : This was 400. I think it shd be 500.  TBD
		return
	}

	// respond with an ID that can be used to retrieve the file
	ur := &UploadResponse{
		ID: id,
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

	err = ss.storageClient.removeFileByID(fileId)
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
	err = ss.storageClient.getFileIntoResponse(fileId, w)
	if err != nil {
		log.WithError(err).Errorf("Error getting item id '%v'", fileId)
		if err == ErrNotFound {
			http.Error(w, "Error retrieving item: not found", 404)
		} else if err == ErrRetrievingItem {
			http.Error(w, "Error retrieving item", 400)
		} else if err == ErrOpeningItem {
			http.Error(w, "Error opening item", 400)
		} else if err == ErrWritingFileIntoResponse {
			http.Error(w, "Error writing response", 500)
		}
		return
	}
}

func MakeStorageService(storageClient *StowClient, port int) *StorageService {
	return &StorageService{
		storageClient: storageClient,
		port:          port,
	}
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
	// initialize logger
	log.SetLevel(log.InfoLevel)

	// create a storage client
	storageClient, err := MakeStowClient(storageType, storagePath, containerName)
	if err != nil {
		log.Panicf("Error initializing storage: %v", err)
	}

	// create http handlers
	storageService := MakeStorageService(storageClient, port)
	go storageService.Start(port)

	// get the prune interval and start the archive pruner
	pruneInterval, err := strconv.Atoi(os.Getenv("PRUNE_INTERVAL"))
	if err != nil {
		pruneInterval = defaultPruneInterval
	}
	pruner := MakeArchivePruner(storageClient, pruneInterval)
	go pruner.Start()

	log.Info("Storage service started..")
	return storageService
}
