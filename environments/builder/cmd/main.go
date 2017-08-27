package main

import (
	"log"
	"net/http"
	"os"

	builder "github.com/fission/fission/environments/builder"
)

// Usage: builder <shared volume path>
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
	builder := builder.MakeBuilder(dir)
	mux := http.NewServeMux()
	mux.HandleFunc("/", builder.Handler)
	http.ListenAndServe(":8000", mux)
}
