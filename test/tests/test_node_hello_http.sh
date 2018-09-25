#!/bin/bash

set -euo pipefail

test_fn() {
    echo "Doing an HTTP GET on the function's route"
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

ROOT=$(dirname $0)/../..

fn=nodejs-hello-$(date +%N)

cleanup() {
    log "Cleaning up..."
    fission env delete --name nodejs || true
    fission fn delete --name $fn || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

# Create a hello world function in nodejs, test it with an http trigger
log "Pre-test cleanup"
fission env delete --name nodejs || true

log "Creating nodejs env"
fission env create --name nodejs --image fission/node-env

log "Creating function"
fission fn create --name $fn --env nodejs --code $ROOT/examples/nodejs/hello.js

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 3

timeout 60 bash -c "test_fn $fn 'hello'"

routeid=$(fission route list|grep "$fn"|awk '{print $1}')
fission route delete --name $routeid || true

log "All done."
