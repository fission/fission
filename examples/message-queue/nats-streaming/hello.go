package main

import (
	"log"
	"net/http"
)

// Handler is the entry point for this fission function
func Handler(w http.ResponseWriter, r *http.Request) {
	log.Print("Hello, world!")
	msg := "Hello, world!\n"
	w.Write([]byte(msg))
}
