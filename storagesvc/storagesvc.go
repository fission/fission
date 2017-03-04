/*
Copyright 2016 The Fission Authors.

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
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/graymeta/stow"
	_ "github.com/graymeta/stow/local"
	"github.com/satori/go.uuid"
)

type (
	storageConfig struct {
		storageType string
		localPath   string
		// other stuff, such as google or s3 credentials, bucket names etc
	}

	storageSvc struct {
		config    storageConfig
		container stow.Container
		port      int
	}

	uploadResponse struct {
		Id string `json:"id"`
	}
)

// Handle multipart file uploads.
func (ss *storageSvc) uploadHandler(w http.ResponseWriter, r *http.Request) {
	// handle upload
	r.ParseMultipartForm(0)
	file, handler, err := r.FormFile("uploadfile")
	if err != nil {
		http.Error(w, "missing upload file", 400)
		return
	}
	defer file.Close()

	// We want the file size, which is separate from the content
	// length, the content length being the size of the encoded
	// file in the HTTP request.
	fileSizeS := r.Header["X-File-Size"][0]
	fileSize, err := strconv.Atoi(fileSizeS)
	if err != nil {
		log.Printf("Error parsing x-file-size: '%v'", fileSizeS)
		http.Error(w, "missing or bad X-File-Size header", 400)
		return
	}

	// TODO: metadata. e.g. environment; function id; version
	fileMetadata := make(map[string]interface{})
	fileMetadata["filename"] = handler.Filename

	// should we just use handler.Filename? what are the constraints here?
	uploadName := uuid.NewV4().String()

	// save the file to the storage backend
	item, err := ss.container.Put(uploadName, file, int64(fileSize), fileMetadata)
	if err != nil {
		log.Printf("Error saving uploaded file: '%v'", fileSizeS)
		http.Error(w, "Error saving uploaded file", 400)
		return
	}

	// respond with an ID that can be used to retrieve the file
	ur := &uploadResponse{
		Id: item.ID(),
	}
	resp, err := json.Marshal(ur)
	if err != nil {
		http.Error(w, "Error marshaling response", 500)
	}
	w.Write(resp)
}

func (ss *storageSvc) downloadHandler(w http.ResponseWriter, r *http.Request) {
	// get id from request
	fileId := r.FormValue("id")
	if len(fileId) == 0 {
		http.Error(w, "missing file id", 400)
	}

	// Get the file (called "item" in stow's jargon), open it,
	// stream it to response

	item, err := ss.container.Item(fileId)
	if err != nil {
		log.Printf("Error getting item id '%v': %v", fileId, err)
		// TODO better http errors based on err
		http.Error(w, "Error retrieving item", 400)
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

func setupStorage(sc *storageConfig) stow.Location {
	if sc.storageType != "local" {
		panic("Storage types other than 'local' are not implemented")
	}
	cfg := stow.ConfigMap{"path": sc.localPath}
	loc, err := stow.Dial(sc.storageType, cfg)
	if err != nil {
		log.Panicf("Error initializing storage: %v", err)
	}

	return loc
}

func Start(storageType string, storagePath string, port int) {
	// setup storage
	// setup handlers
}
