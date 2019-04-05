#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

ROOT=$(dirname $0)/../..
TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

nodejs_env=nodejs-$TEST_ID
fn0=nodejs-hello-0-$TEST_ID
fn1=nodejs-hello-1-$TEST_ID

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
fission env create --name $nodejs_env --image $NODE_RUNTIME_IMAGE --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256

# TODO Imporve test code by reusing common blocks

log "Creating function, testing for cold start with MinScale 0"
fission fn create --name $fn0 --env $nodejs_env --code $ROOT/examples/nodejs/hello.js --minscale 0 --maxscale 4 --executortype newdeploy

log "Creating route"
fission route create --function $fn0 --url /$fn0 --method GET

log "Waiting for router & newdeploy deployment creation"
sleep 5

log "Doing an HTTP GET on the function's route"
response0=$(curl http://$FISSION_ROUTER/$fn0)

log "Checking for valid response"
echo $response0 | grep -i hello

log "Creating function, testing for warm start with MinScale 1"
fission fn create --name $fn1 --env $nodejs_env --code $ROOT/examples/nodejs/hello.js --minscale 1 --maxscale 4 --executortype newdeploy

log "Creating route"
fission route create --function $fn1 --url /$fn1 --method GET

log "Waiting for router & newdeploy deployment creation"
sleep 5

log "Doing an HTTP GET on the function's route"
response1=$(curl http://$FISSION_ROUTER/$fn1)

log "Checking for valid response"
echo $response1 | grep -i hello

log "NewDeploy ExecutorType: All done."
