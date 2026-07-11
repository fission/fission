// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"log"
	"net/http"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/metrics"
)

// muxForHandler builds a single-route httpmux handler (metrics wired, like the
// public listener) for the mutable-swap test.
func muxForHandler(h http.HandlerFunc) http.Handler {
	m := httpmux.New(httpmux.WithMetrics(metrics.HTTPRecorder{}))
	m.HandleFunc("/", h)
	return m.Handler()
}

func OldHandler(responseWriter http.ResponseWriter, request *http.Request) {
	_, err := responseWriter.Write([]byte("old handler"))
	if err != nil {
		log.Fatal(err)
	}
}
func NewHandler(responseWriter http.ResponseWriter, request *http.Request) {
	_, err := responseWriter.Write([]byte("new handler"))
	if err != nil {
		log.Fatal(err)
	}
}

func verifyRequest(expectedResponse string) {
	targetURL := "http://localhost:3333"
	testRequest(targetURL, expectedResponse)
}

func spamServer(quit chan bool) {
	i := 0
	for {
		select {
		case <-quit:
			return
		default:
			i = i + 1
			resp, err := http.Get("http://localhost:3333")
			if err != nil {
				log.Panicf("failed to make get request %v: %v", i, err)
			}
			resp.Body.Close()
		}
	}
}

func TestMutableMux(t *testing.T) {
	mgr := &errgroup.Group{}
	t.Cleanup(func() { _ = mgr.Wait() })

	// make a simple mutable router
	log.Print("Create mutable router")
	logger := loggerfactory.GetLogger()

	mr := newMutableRouter(logger, muxForHandler(OldHandler))
	ctx := t.Context()

	// start http server
	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{Name: "router", Addr: "3333", Handler: mr})
		return nil
	})

	// continuously make requests, panic if any fails
	time.Sleep(100 * time.Millisecond)
	q := make(chan bool)

	mgr.Go(func() error {
		spamServer(q)
		return nil
	})

	time.Sleep(5 * time.Millisecond)

	// connect and verify old handler
	log.Print("Verify old handler")
	verifyRequest("old handler")

	// change the muxer
	log.Print("Change mux router")
	mr.updateRouter(muxForHandler(NewHandler))

	// connect and verify the new handler
	log.Print("Verify new handler")
	verifyRequest("new handler")

	q <- true
	time.Sleep(100 * time.Millisecond)
}
