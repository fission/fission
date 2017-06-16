/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package poolmgr

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"

	"github.com/fission/fission"
	"github.com/fission/fission/cache"
	controllerclient "github.com/fission/fission/controller/client"
)

type API struct {
	poolMgr     *GenericPoolManager
	functionEnv *cache.Cache // map[fission.Metadata]fission.Environment
	function    *cache.Cache // map[fission.Metadata]fission.Function
	fsCache     *functionServiceCache
	controller  *controllerclient.Client

	//functionService *cache.Cache // map[fission.Metadata]*funcSvc
	//urlFuncSvc      *cache.Cache // map[string]*funcSvc
}

func MakeAPI(gpm *GenericPoolManager, controller *controllerclient.Client, fsCache *functionServiceCache) *API {
	return &API{
		poolMgr:     gpm,
		functionEnv: cache.MakeCache(time.Minute, 0),
		function:    cache.MakeCache(time.Minute, 0),
		fsCache:     fsCache,
		controller:  controller,
	}
}

func (api *API) getServiceForFunctionApi(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}

	// get function metadata
	m := fission.Metadata{}
	err = json.Unmarshal(body, &m)
	if err != nil {
		http.Error(w, "Failed to parse request", 400)
		return
	}

	serviceName, err := api.getServiceForFunction(&m)
	if err != nil {
		code, msg := fission.GetHTTPError(err)
		log.Printf("Error: %v: %v", code, msg)
		http.Error(w, msg, code)
	}

	w.Write([]byte(serviceName))
}

func (api *API) getFunctionInfo(m *fission.Metadata) (*fission.Environment, *fission.Function, error) {
	// Cached ?
	e, err := api.functionEnv.Get(*m)
	f, err := api.function.Get(*m)
	if err == nil {
		return e.(*fission.Environment), f.(*fission.Function), nil
	}

	// Cache miss -- get func from controller
	log.Printf("[%v] getting function from controller", m)
	fnc, err := api.controller.FunctionGet(m)
	if err != nil {
		return nil, nil, err
	}

	// Get env from metadata
	log.Printf("[%v] getting env from controller", m)
	env, err := api.controller.EnvironmentGet(&fnc.Environment)
	if err != nil {
		return nil, nil, err
	}

	// cache for future
	api.functionEnv.Set(*m, env)
	api.function.Set(*m, fnc)

	return env, fnc, nil
}

func (api *API) getServiceForFunction(m *fission.Metadata) (string, error) {
	// Make sure we have the full metadata.  This ensures that
	// poolmgr does not implicitly interpret empty-UID as latest
	// version.
	if len(m.Uid) == 0 {
		return "", fission.MakeError(fission.ErrorInvalidArgument,
			fmt.Sprintf("invalid metadata for function %v", m.Name))
	}

	// Check function -> svc cache
	log.Printf("[%v] Checking for cached function service", m.Name)
	fsvc, err := api.fsCache.GetByFunction(m)
	if err == nil {
		// Cached, return svc address
		return fsvc.getAddress(), nil
	}

	// None exists, so create a new funcSvc:
	log.Printf("[%v] No cached function service found, creating one", m.Name)

	// from Func -> get Env
	log.Printf("[%v] getting environment for function", m.Name)
	env, fnc, err := api.getFunctionInfo(m)
	if err != nil {
		return "", err
	}

	// from Env -> get GenericPool
	log.Printf("[%v] getting generic pool for env", m.Name)
	pool, err := api.poolMgr.GetPool(env)
	if err != nil {
		return "", err
	}

	// from GenericPool -> get one function container
	// (this also adds to the cache)
	log.Printf("[%v] getting function service from pool", m.Name)
	funcSvc, err := pool.GetFuncSvc(m, fnc, env)
	if err != nil {
		return "", err
	}

	return funcSvc.getAddress(), nil
}

// find funcSvc and update its atime
func (api *API) tapService(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}
	svcName := string(body)
	svcHost := strings.TrimPrefix(svcName, "http://")

	err = api.fsCache.TouchByAddress(svcHost)
	if err != nil {
		log.Printf("funcSvc tap error: %v", err)
		http.Error(w, "Not found", 404)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (api *API) Serve(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/v1/getServiceForFunction", api.getServiceForFunctionApi).Methods("POST")
	r.HandleFunc("/v1/tapService", api.tapService).Methods("POST")

	address := fmt.Sprintf(":%v", port)
	log.Printf("starting poolmgr at port %v", port)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
