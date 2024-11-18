package main

import (
	"html"
	"io"
	"log"
	"net/http"
)

// Handler is the entry point for this fission function
func Handler(w http.ResponseWriter, r *http.Request) { //nolint:golint,unused,deadcode
	bytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = w.Write([]byte(html.EscapeString(string(bytes))))
	if err != nil {
		log.Fatal(err)
	}
}
