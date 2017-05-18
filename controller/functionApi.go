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

package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/mux"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/logdb"
)

func (api *API) FunctionApiList(w http.ResponseWriter, r *http.Request) {
	funcs, err := api.FunctionStore.List()
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(funcs)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var f fission.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	dec, err := base64.StdEncoding.DecodeString(f.Code)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	f.Code = string(dec)

	uid, err := api.FunctionStore.Create(&f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	m := &fission.Metadata{Name: f.Name, Uid: uid}
	resp, err := json.Marshal(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiGet(w http.ResponseWriter, r *http.Request) {
	var m fission.Metadata

	vars := mux.Vars(r)
	m.Name = vars["function"]
	m.Uid = r.FormValue("uid") // empty if uid is absent
	raw := r.FormValue("raw")  // just the code

	f, err := api.FunctionStore.Get(&m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	var resp []byte
	if raw != "" {
		resp = []byte(f.Code)
	} else {
		f.Code = base64.StdEncoding.EncodeToString([]byte(f.Code))
		resp, err = json.Marshal(f)
		if err != nil {
			api.respondWithError(w, err)
			return
		}
	}
	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	funcName := vars["function"]

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var f fission.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	if funcName != f.Metadata.Name {
		err = fission.MakeError(fission.ErrorInvalidArgument, "Function name doesn't match URL")
		api.respondWithError(w, err)
		return
	}

	dec, err := base64.StdEncoding.DecodeString(f.Code)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	f.Code = string(dec)

	uid, err := api.FunctionStore.Update(&f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	m := &fission.Metadata{Name: f.Name, Uid: uid}
	resp, err := json.Marshal(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var m fission.Metadata
	m.Name = vars["function"]

	m.Uid = r.FormValue("uid") // empty if uid is absent
	if len(m.Uid) == 0 {
		log.WithFields(log.Fields{"function": m.Name}).Info("Deleting all versions")
	}

	err := api.FunctionStore.Delete(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, []byte(""))
}

func (api *API) FunctionLogsApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	fnName := vars["function"]

	var detail, follow bool
	if val, err := strconv.ParseBool(r.FormValue("detail")); err == nil {
		detail = val
	}
	if val, err := strconv.ParseBool(r.FormValue("follow")); err == nil {
		follow = val
	}

	fnPod := r.FormValue("pod")

	logDB, err := logdb.GetLogDB(api.DBConfig)
	if err != nil {
		w.Write([]byte("failed to connect log database"))
		return
	}

	requestChan := make(chan struct{})
	responseChan := make(chan struct{})
	ctx := context.Background()

	fMetadata, err := api.FunctionStore.Get(&fission.Metadata{Name: fnName})
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	t := time.Unix(0, 0*int64(time.Millisecond))

	go func(ctx context.Context, requestChan, responseChan chan struct{}) {
		for {
			select {
			case <-requestChan:
				logFilter := logdb.LogFilter{
					Pod:      fnPod,
					Function: fMetadata.Name,
					FuncUid:  fMetadata.Uid,
					Since:    t,
				}
				logEntries, err := logDB.GetLogs(logFilter)
				if err != nil {
					// fatal("failed to query logs")
					api.respondWithError(w, err)
				}
				for _, logEntry := range logEntries {
					var logMsg string
					if detail {
						logMsg = fmt.Sprintf("Timestamp: %s\nNamespace: %s\nFunction Name: %s\nFunction ID: %s\nPod: %s\nContainer: %s\nStream: %s\nLog: %s\n---\n",
							logEntry.Timestamp, logEntry.Namespace, logEntry.FuncName, logEntry.FuncUid, logEntry.Pod, logEntry.Container, logEntry.Stream, logEntry.Message)
					} else {
						logMsg = fmt.Sprintf("[%s] %s\n", logEntry.Timestamp, logEntry.Message)
					}
					w.Write([]byte(logMsg))
					// force flush out bytes in buffer
					w.(http.Flusher).Flush()
					t = logEntry.Timestamp
				}
				responseChan <- struct{}{}
			case <-ctx.Done():
				return
			}
		}
	}(ctx, requestChan, responseChan)

	for {
		requestChan <- struct{}{}
		<-responseChan
		if !follow {
			ctx.Done()
			return
		}
	}
}

func (api *API) FunctionPodsApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	fnName := vars["function"]

	logDB, err := logdb.GetLogDB(api.DBConfig)
	if err != nil {
		api.respondWithError(w, err)
	}

	fMetadata, err := api.FunctionStore.Get(&fission.Metadata{Name: fnName})
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	logFilter := logdb.LogFilter{
		Function: fMetadata.Name,
		FuncUid:  fMetadata.Uid,
	}
	pods, err := logDB.GetPods(logFilter)
	if err != nil {
		api.respondWithError(w, err)
	}
	for _, pod := range pods {
		w.Write([]byte(fmt.Sprintf("%s\n", pod)))
		// force flush out bytes in buffer
		w.(http.Flusher).Flush()
	}
}
