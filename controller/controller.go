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

package controller

import (
	"context"
	"log"

	"github.com/fission/fission"
	"github.com/fission/fission/canaryconfigmgr"
	"github.com/fission/fission/crd"
)

func Start(port int, prometheusSvc string) {
	// setup a signal handler for SIGTERM
	fission.SetupStackTraceHandler()

	fc, kc, apiExtClient, err := crd.MakeFissionClient()
	if err != nil {
		log.Fatalf("Failed to connect to K8s API: %v", err)
	}

	err = crd.EnsureFissionCRDs(apiExtClient)
	if err != nil {
		log.Fatalf("Failed to create fission CRDs: %v", err)
	}

	err = fc.WaitForCRDs()
	if err != nil {
		log.Fatalf("Error waiting for CRDs: %v", err)
	}

	// create canary config manager
	canaryCfgMgr, err := canaryconfigmgr.MakeCanaryConfigMgr(fc, kc, fc.GetCrdClient(), prometheusSvc)
	if err != nil {
		log.Fatalf("Failed to start canary config manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canaryCfgMgr.Run(ctx)

	api, err := MakeAPI()
	if err != nil {
		log.Fatalf("Failed to start controller: %v", err)
	}
	api.Serve(port)
}
