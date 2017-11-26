package main

import (
	"net/http"
	"os"

	"github.com/fission/fission/environments/fetcher"
)

// Usage: fetcher <shared volume path>
func main() {
	funcDir := os.Args[1]
	secretDir := os.Args[2]
	configDir := os.Args[3]
	
	fetcher := fetcher.MakeFetcher(funcDir, secretDir, configDir)
	mux := http.NewServeMux()
	mux.HandleFunc("/", fetcher.FetchHandler)
	mux.HandleFunc("/upload", fetcher.UploadHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	http.ListenAndServe(":8000", mux)
}
