package main

import (
	"log"
	"net/http"
)

// Handler is the entry point for this fission function
func Handler(w http.ResponseWriter, r *http.Request) { //nolint:golint,unused,deadcode
	msg := "Hello, CNCF Webinar!\n"
	_, err := w.Write([]byte(msg))
	if err != nil {
		log.Fatal(err)
	}
}
