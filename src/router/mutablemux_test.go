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

package router

import (
	"testing"
	"net/http"
	"github.com/gorilla/mux"
	"log"
	"io/ioutil"
	"time"
)

func OldHandler(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Write([]byte("old handler"))
}
func NewHandler(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Write([]byte("new handler"))
}

func verifyRequest(expectedResponse string) {
	resp, err := http.Get("http://localhost:3333")
	if (err != nil) {
		log.Panic("failed make get request")
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if (err != nil) {
		log.Panic("failed to read response")
	}

	bodyStr := string(body)
	log.Printf("Server responded with %v", bodyStr)
	if (bodyStr != expectedResponse) {
		log.Panic("Unexpected response")
	}
}

func startServer(mr *mutableRouter) {
	http.ListenAndServe(":3333", mr)
}

func spamServer() {
	i := 0
	for {
		i = i + 1
		resp, err := http.Get("http://localhost:3333")
		if (err != nil) {
			log.Panicf("failed make get request %v", i)
		}
		resp.Body.Close()
		log.Printf("request count = %v", i)
	}
}

func TestMutableMux(t *testing.T) {
	// make a simple mutable router
	log.Print("Create mutable router")
	muxRouter := mux.NewRouter()
	muxRouter.HandleFunc("/", OldHandler)
	mr := NewMutableRouter(muxRouter)
	
	// start http server
	log.Print("Start http server")
	go startServer(mr)
	
	// continuously make requests, panic if any fails
	go spamServer()
	go spamServer()
	go spamServer()

	time.Sleep(5 * time.Millisecond)

	// connect and verify old handler
	log.Print("Verify old handler")
	verifyRequest("old handler")

	// change the muxer
	log.Print("Change mux router")
	newMuxRouter := mux.NewRouter()
	newMuxRouter.HandleFunc("/", NewHandler)
	mr.UpdateRouter(newMuxRouter)

	// connect and verify the new handler
	log.Print("Verify new handler")
	verifyRequest("new handler")
	
	time.Sleep(5 * time.Millisecond)

	// all done
	log.Print("ok")
}
