#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../../..

cleanup() {
    fission fn delete --name hello-go-poolmgr || true
    fission fn delete --name hello-go-nd || true
    fission env delete --name go || true
    rm $ROOT/examples/go/vendor-example/vendor.zip || true
}

wait_for_builder() {
    JSONPATH='{range .items[*]}{@.metadata.name}:{range @.status.conditions[*]}{@.type}={@.status};{end}{end}'

    # wait for tiller ready
    while true; do
      kubectl --namespace fission-builder get pod -l envName=go -o jsonpath="$JSONPATH" | grep "Ready=True"
      if [[ $? -eq 0 ]]; then
          break
      fi
      sleep 5
    done
}

waitBuild() {
    log "Waiting for builder manager to finish the build"

    while true; do
      kubectl --namespace default get packages $1 -o jsonpath='{.status.buildstatus}'|grep succeeded
      if [[ $? -eq 0 ]]; then
          break
      fi
    done
}

test_fn() {
    log "Checking for valid response"

    while true; do
      response0=$(curl http://$FISSION_ROUTER/$1)
      log $response0 | grep -i $2
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
}

export -f wait_for_builder
export -f waitBuild
export -f test_fn

cd $ROOT/examples/go

trap cleanup EXIT

GO_RUNTIME_IMAGE=${GO_RUNTIME_IMAGE:-gcr.io/fission-ci/go-env:test}
GO_BUILDER_IMAGE=${GO_BUILDER_IMAGE:-gcr.io/fission-ci/go-env-builder:test}

log "Creating environment for Golang"
fission env create --name go --image $GO_RUNTIME_IMAGE --builder $GO_BUILDER_IMAGE --period 5

timeout 90 bash -c "wait_for_builder"

pkgName=$(fission package create --src hello.go --env go| cut -f2 -d' '| tr -d \')

# wait for build to finish at most 90s
timeout 90 bash -c "waitBuild $pkgName"

log "Creating pool manager & new deployment function for Golang"
fission fn create --name hello-go-poolmgr --env go --pkg $pkgName --entrypoint Handler
fission fn create --name hello-go-nd --env go --pkg $pkgName --entrypoint Handler --executortype newdeploy

log "Creating route for new deployment function"
fission route create --function hello-go-poolmgr --url /hello-go-poolmgr --method GET
fission route create --function hello-go-nd --url /hello-go-nd --method GET

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function"
timeout 60 bash -c "test_fn hello-go-poolmgr 'Hello'"

log "Testing new deployment function"
timeout 60 bash -c "test_fn hello-go-nd 'Hello'"

# Create zip file without top level directory (vendor-example)
cd vendor-example && zip -r vendor.zip *

pkgName=$(fission package create --src vendor.zip --env go| cut -f2 -d' '| tr -d \')

# wait for build to finish at most 90s
timeout 90 bash -c "waitBuild $pkgName"

log "Update function package"
fission fn update --name hello-go-poolmgr --pkg $pkgName
fission fn update --name hello-go-nd --pkg $pkgName

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function with new package"
timeout 60 bash -c "test_fn hello-go-poolmgr 'vendor'"

log "Testing new deployment function with new package"
timeout 60 bash -c "test_fn hello-go-nd 'vendor'"
