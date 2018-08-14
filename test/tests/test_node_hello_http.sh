#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..

fn=nodejs-hello-$(date +%N)

cleanup() {
    log "Cleaning up..."
    fission env delete --name nodejs || true
    fission fn delete --name $fn || true
}

cleanup
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
#trap "fission env delete --name nodejs" EXIT

log "Creating function"
fission fn create --name $fn --env nodejs --code $ROOT/examples/nodejs/hello.js
#trap "fission fn delete --name $fn" EXIT

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 3

log "Doing an HTTP GET on the function's route"
response=$(curl http://$FISSION_ROUTER/$fn)

log "Checking for valid response"
echo $response | grep -i hello

routeid=$(fission route list|grep "$fn"|awk '{print $1}')
fission route delete --name $routeid || true

log "All done."
