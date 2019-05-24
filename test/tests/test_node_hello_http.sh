#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

ROOT=$(dirname $0)/../..

env=nodejs-$TEST_ID
fn=nodejs-hello-$TEST_ID

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

# Create a hello world function in nodejs, test it with an http trigger
log "Creating nodejs env"
fission env create --name $env --image $NODE_RUNTIME_IMAGE

log "Creating function"
fission fn create --name $fn --env $env --code $ROOT/examples/nodejs/hello.js

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 3

log "Doing an HTTP GET on the function's route"
response=$(curl --retry 5 http://$FISSION_ROUTER/$fn)

log "Checking for valid response"
echo $response | grep -i hello

log "All done."
