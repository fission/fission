package controller

import (
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/fission/fission/redis"
)

func (a *API) ReplayByReqUID(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	queriedID := vars["reqUID"]

	routerUrl := fmt.Sprintf("http://router.%v", a.podNamespace)

	resp, err := redis.ReplayByReqUID(routerUrl, queriedID)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, resp)
}