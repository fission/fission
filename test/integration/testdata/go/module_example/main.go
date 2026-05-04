package main

import (
	"log"
	"net/http"

	"golang.org/x/example/stringutil"
)

// Handler is the entry point for this fission function
func Handler(w http.ResponseWriter, r *http.Request) {
	msg := stringutil.Reverse(stringutil.Reverse("Vendor Example Test"))
	_, err := w.Write([]byte(msg))
	if err != nil {
		log.Printf("Error writing response: %v", err)
	}
}
