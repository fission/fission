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

	"github.com/platform9/fission"
	"github.com/platform9/fission/cache"
	controllerclient "github.com/platform9/fission/controller/client"
)

type funcSvc struct {
	function    *fission.Metadata    // function this pod/service is for
	environment *fission.Environment // env it was obtained from
	serviceName string               // name of k8s svc

	reaped bool // if true, the pod has been deleted

	ctime time.Time
	atime time.Time
}

type API struct {
	poolMgr         *GenericPoolManager
	functionEnv     *cache.Cache // map[fission.Metadata]fission.Environment
	functionService *cache.Cache // map[fission.Metadata]*funcSvc
	urlFuncSvc      *cache.Cache // map[string]*funcSvc
	controller      *controllerclient.Client
}

func MakeAPI(gpm *GenericPoolManager, controller *controllerclient.Client) *API {
	return &API{
		poolMgr:         gpm,
		functionEnv:     cache.MakeCache(0),
		functionService: cache.MakeCache(time.Minute),
		urlFuncSvc:      cache.MakeCache(time.Minute),
		controller:      controller,
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

func (api *API) getFunctionEnv(m *fission.Metadata) (*fission.Environment, error) {
	var env *fission.Environment

	// Cached ?
	result, err := api.functionEnv.Get(*m)
	if err == nil {
		env = result.(*fission.Environment)
		return env, nil
	}

	// Cache miss -- get func from controller
	log.Printf("[%v] getting function from controller", m)
	f, err := api.controller.FunctionGet(m)
	if err != nil {
		return nil, err
	}

	// Get env from metadata
	log.Printf("[%v] getting env from controller", m)
	env, err = api.controller.EnvironmentGet(&f.Environment)
	if err != nil {
		return nil, err
	}

	// cache for future
	api.functionEnv.Set(*m, env)

	return env, nil
}

func (api *API) getServiceForFunction(m *fission.Metadata) (string, error) {
	// Check function -> svc map
	log.Printf("[%v] Checking for cached function service", m.Name)
	result, err := api.functionService.Get(*m)
	if err == nil {
		//    Ok:     return svc name
		svc := result.(*funcSvc)
		svc.atime = time.Now()
		return svc.serviceName, nil
	}

	//    None exists, so create a new funcSvc:
	log.Printf("[%v] No cached function service found, creating one", m.Name)

	//      from Func -> get Env
	log.Printf("[%v] getting environment for function", m.Name)
	env, err := api.getFunctionEnv(m)
	if err != nil {
		return "", err
	}

	//      from Env -> get GenericPool
	log.Printf("[%v] getting generic pool for env", m.Name)
	pool, err := api.poolMgr.GetPool(env)
	if err != nil {
		return "", err
	}

	//      from GenericPool -> get one function container
	log.Printf("[%v] getting function service from pool", m.Name)
	funcSvc, err := pool.GetFuncSvc(m)
	if err != nil {
		return "", err
	}

	// add to cache
	err = api.functionService.Set(*m, funcSvc)
	if err != nil {
		// log and ignore error
		log.Printf("Error caching function service: %v", err)
	}

	// cache by svc hostname, for tapService()
	err = api.urlFuncSvc.Set(funcSvc.serviceName, funcSvc)
	if err != nil {
		// log and ignore error
		log.Printf("Error caching function service by name: %v", err)
	}

	return funcSvc.serviceName, nil
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

	log.Printf("tap svc: %v", svcHost)

	funcSvcI, err := api.urlFuncSvc.Get(svcHost)
	if err != nil {
		http.Error(w, "Not found", 404)
		return
	}

	(funcSvcI.(*funcSvc)).atime = time.Now()

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
