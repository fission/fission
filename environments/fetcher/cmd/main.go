package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/fission/fission/environments/fetcher"
)

// Usage: fetcher <shared volume path>
func main() {
	flag.Usage = fetcherUsage
	fetchPayload := flag.String("fetch-request", "", "JSON Payload for fetch request")
	loadPayload := flag.String("load-request", "", "JSON payload for Load request")
	specializeOnStart := flag.Bool("specialize-on-startup", false, "Flag to activate specialize process at pod starup")
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	dir := flag.Arg(0)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, os.ModeDir|0700)
			if err != nil {
				log.Fatalf("Error creating directory: %v", err)
			}
		}
	}

	_fetcher := fetcher.MakeFetcher(dir)

	if *specializeOnStart {
		// Fetch code
		var fetchReq fetcher.FetchRequest
		err := json.Unmarshal([]byte(*fetchPayload), &fetchReq)
		if err != nil {
			log.Fatalf("Error parsing fetch request: %v", err)
		}
		err, _ = _fetcher.Fetch(fetchReq)
		if err != nil {
			log.Fatalf("Error fetching: %v", err)
		}
		// Specialize the pod
		resp, err := http.Post("http://localhost:8888/specialize", "application/json", bytes.NewReader([]byte(*loadPayload)))
		if err != nil {
			log.Fatalf("Error specializing pod: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Fatalf("Specializing pod failed: %v", resp)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", _fetcher.FetchHandler)
	mux.HandleFunc("/upload", _fetcher.UploadHandler)
	http.ListenAndServe(":8000", mux)
}

func fetcherUsage() {
	fmt.Printf("Usage: fetcher [OPTIONAL] -specialize-on-startup [OPTIONAL] -fetch-request <json> <shared volume path> \n")
}
