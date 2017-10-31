package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/fission/fission"
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
		specializePod(_fetcher, fetchPayload, loadPayload)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", _fetcher.FetchHandler)
	mux.HandleFunc("/upload", _fetcher.UploadHandler)
	http.ListenAndServe(":8000", mux)
}

func fetcherUsage() {
	fmt.Printf("Usage: fetcher [OPTIONAL] -specialize-on-startup [OPTIONAL] -fetch-request <json> <shared volume path> \n")
}

func specializePod(_fetcher *fetcher.Fetcher, fetchPayload *string, loadPayload *string) {
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
	fmt.Println("EnvVersion:", os.Getenv("ENV_VERSION"))
	envVersion, err := strconv.Atoi(os.Getenv("ENV_VERSION"))
	if err != nil {
		log.Fatalf("Error parsing environment version %v", err)
	}
	fmt.Println("Specialize Payload:", loadPayload)
	maxRetries := 30
	for i := 0; i < maxRetries; i++ {
		var resp2 *http.Response
		if envVersion == 2 {
			resp2, err = http.Post("http://localhost:8888/v2/specialize", "application/json", bytes.NewReader([]byte{}))
		} else {
			resp2, err = http.Post("http://localhost:8888/specialize", "application/json", bytes.NewReader([]byte{}))
		}
		log.Printf("Failed to specialize pod: %v", err)
		if err == nil && resp2.StatusCode < 300 {
			// Success
			resp2.Body.Close()
			fmt.Println("Pod Specialization worked in #", i)
			break
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
	}

}
