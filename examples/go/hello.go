package main

import (
	"fmt"
	"github.com/zbiljic/rands"
	"net/http"
)

// Handler is the entry point for this fission function
func Handler(w http.ResponseWriter, r *http.Request) {
	randomString := rands.AlphabeticString(5)
	msg := fmt.Sprintf("Hello, world %v!\n", randomString)
	w.Write([]byte(msg))
}
