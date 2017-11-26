package main

import (
	"net/http"
	"flag"
	"github.com/fission/fission/environments/fetcher"
)

// Usage: fetcher <shared volume path>
func main() {
	flag.Parse()
	funcDir := flag.Arg(0)
	secretDir := flag.Arg(1)
	configDir := flag.Arg(2)
	
	fetcher := fetcher.MakeFetcher(funcDir, secretDir, configDir)
	mux := http.NewServeMux()
	mux.HandleFunc("/", fetcher.FetchHandler)
	mux.HandleFunc("/upload", fetcher.UploadHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	http.ListenAndServe(":8000", mux)
}
