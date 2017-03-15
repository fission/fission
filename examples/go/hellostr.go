package main

import (
	"fmt"
	"net/http"
	"github.com/fission/fission/environments/go/runtime"
)

func Handler(ctx runtime.Context, w http.ResponseWriter, r *http.Request) {
	params := runtime.GetParams(ctx)
	fmt.Printf("Req: %s %s\n", r.Host, r.URL.Path)
	msg := "Hello, " + params["name"]
	w.Write([]byte(msg))
}
