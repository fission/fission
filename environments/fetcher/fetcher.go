package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fission/fission"
)

type (
	FetchRequest struct {
		Url      string `json:"url"`
		Filename string `json:"filename"`
	}

	Fetcher struct {
		sharedVolumePath string
		ready            atomic.Value
	}
)

func MakeFetcher(sharedVolumePath string) *Fetcher {
	f := &Fetcher{
		sharedVolumePath: sharedVolumePath,
	}
	f.ready.Store(false)
	return f
}

func (fetcher *Fetcher) handleFetchRequest(req *FetchRequest) error {
	// fetch the file and save it to tmp path
	resp, err := http.Get(req.Url)
	if err != nil {
		e := fmt.Errorf("Failed to fetch from url: %v", err)
		return e
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		e := fmt.Errorf("Failed to read from url: %v", err)
		return e
	}
	tmpFile := req.Filename + ".tmp"
	tmpPath := filepath.Join(fetcher.sharedVolumePath, tmpFile)
	err = ioutil.WriteFile(tmpPath, body, 0600)
	if err != nil {
		e := fmt.Errorf("Failed to write file: %v", err)
		return e
	}

	// TODO: add signature verification

	// move tmp file to requested filename
	err = os.Rename(tmpPath, filepath.Join(fetcher.sharedVolumePath, req.Filename))
	if err != nil {
		e := fmt.Errorf("Failed to move file: %v", err)
		return e
	}

	return nil
}

func (fetcher *Fetcher) fetchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", 404)
		return
	}

	// parse request
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body")
		http.Error(w, err.Error(), 500)
		return
	}
	var req FetchRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("fetcher request: %v", req)

	err = fetcher.handleFetchRequest(&req)
	if err != nil {
		log.Printf("Error handling fetch requiest: %v", err)
		http.Error(w, err.Error(), 500)
	}

	fetcher.ready.Store(true)

	// all done
	w.WriteHeader(http.StatusOK)
}

func (fetcher *Fetcher) readyHandler(w http.ResponseWriter, r *http.Request) {
	ready := fetcher.ready.Load().(bool)
	if ready {
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, "Not ready", 503)
	}
}

// Specialize the pod we're in, i.e. load the function
func specialize() error {
	specializeUrl := fmt.Sprintf("http://%v:8888/specialize", "localhost")

	// retry the specialize call a few times in case the env server hasn't come up yet
	maxRetries := 20
	for i := 0; i < maxRetries; i++ {
		resp2, err := http.Post(specializeUrl, "text/plain", bytes.NewReader([]byte{}))
		if err == nil && resp2.StatusCode < 300 {
			// Success
			resp2.Body.Close()
			return nil
		}

		// Only retry for the specific case of a connection error.
		if urlErr, ok := err.(*url.Error); ok {
			if netErr, ok := urlErr.Err.(*net.OpError); ok {
				if netErr.Op == "dial" {
					if i < maxRetries-1 {
						time.Sleep(500 * time.Duration(2*i) * time.Millisecond)
						log.Printf("Error connecting to pod (%v), retrying", netErr)
						continue
					}
				}
			}
		}

		if err == nil {
			err = fission.MakeErrorFromHTTP(resp2)
		}
		log.Printf("Failed to specialize pod: %v", err)
		return err
	}
	return nil
}

// Usage: fetcher <shared volume path>
func main() {
	dir := os.Args[1]
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, os.ModeDir|0700)
			if err != nil {
				log.Fatalf("Error creating directory: %v", err)
			}
		}
	}

	fetcher := MakeFetcher(dir)

	// if we have a request via an env var, run that first
	envRequest := os.Getenv("FETCHER_REQUEST")
	fmt.Println("req x", envRequest)
	if len(envRequest) > 0 {
		var req FetchRequest
		err := json.Unmarshal([]byte(envRequest), &req)
		if err != nil {
			log.Fatalf("Error parsing FETCHER_REQUEST env var: %v", err)
		}

		err = fetcher.handleFetchRequest(&req)
		if err != nil {
			log.Fatalf("Error handling fetcher request: %v", err)
		}

		// this pod is being started up without the help of
		// poolmgr: so we call the environment's specialize
		// endpoint from here
		err = specialize()
		if err != nil {
			log.Fatalf("Failed to load the function: %v", err)
		}

		fetcher.ready.Store(true)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ready", fetcher.readyHandler)
	mux.HandleFunc("/", fetcher.fetchHandler)
	http.ListenAndServe(":8000", mux)
}
