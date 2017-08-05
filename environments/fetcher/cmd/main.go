package main

import (
	"log"
	"net/http"
	"os"

	"github.com/fission/fission/environments/fetcher"
)

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
	fetcher := fetcher.MakeFetcher(dir)
	mux := http.NewServeMux()
	mux.HandleFunc("/", fetcher.Handler)
	http.ListenAndServe(":8000", mux)
}
