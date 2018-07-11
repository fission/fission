package main

import (
	"go/vendordir/test"
	"net/http"
)

// Handler is the entry point for this fission function
func Handler(w http.ResponseWriter, r *http.Request) {
	msg := test.Test()
	w.Write([]byte(msg))
}
