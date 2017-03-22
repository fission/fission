package main

import (
	"net/http"
)

func Handler(w http.ResponseWriter, r *http.Request) {
	msg := "Hello, World!"
	w.Write([]byte(msg))
}
