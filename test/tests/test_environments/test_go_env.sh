#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../../..

cleanup() {
    fission fn delete --name hello-go-poolmgr || true
    fission fn delete --name hello-go-nd || true
    fission env delete --name go || true
}

test_fn() {
    echo "Checking for valid response"

    while true; do
      response0=$(curl http://$FISSION_ROUTER/$1)
      echo $response0 | grep -i $2
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
}

export -f test_fn

cd $ROOT/examples/go

log "Creating environment for Golang"
fission env create --name go --image gcr.io/fission-ci/go-env:test --builder gcr.io/fission-ci/go-builder:test --period 5

log "Creating pool manager & new deployment function for Golang"
fission fn create --name hello-go-poolmgr --env go --src hello.go --entrypoint Handler
fission fn create --name hello-go-nd --env go --src hello.go --entrypoint Handler --executortype newdeploy
trap cleanup EXIT

log "Creating route for new deployment function"
fission route create --function hello-go-poolmgr --url /hello-go --method GET
fission route create --function hello-go-nd --url /hello-go --method GET

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function"
timeout 60 bash -c "test_fn hello-go-poolmgr 'Hello, world!'"

log "Testing new deployment function"
timeout 60 bash -c "test_fn hello-go-nd 'Hello'"
