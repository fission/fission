package router

import (
	"io"
	"net/http"
	"testing"

	"github.com/fission/fission/pkg/utils/metrics"
	"github.com/gorilla/mux"
)

func GetRouterWithAuth() *mux.Router {
	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "OK")
	}
	muxRouter := mux.NewRouter()
	muxRouter.Use(metrics.HTTPMetricMiddleware())
	muxRouter.Use(authMiddleware)
	muxRouter.HandleFunc("/auth/login", authLoginHandler()).Methods("POST")
	// We should be able to access health without login
	muxRouter.HandleFunc("/router-healthz", routerHealthHandler).Methods("GET")
	muxRouter.HandleFunc("/test", testHandler).Methods("GET")
	return muxRouter
}

func TestRouterAuth(t *testing.T) {

}
