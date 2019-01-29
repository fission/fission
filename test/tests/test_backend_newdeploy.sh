#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../.. 

cleanup() {
    log "Cleaning up..."
    fission env delete --name nodejs || true
    fission fn delete --name $fn1 || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

# Create a hello world function in nodejs, test it with an http trigger
log "NewDeploy ExecutorType: Pre-test cleanup"
fission env delete --name nodejs || true

log "Creating nodejs env"
fission env create --name nodejs --image fission/node-env --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256
#trap "fission env delete --name nodejs" EXIT

# TODO Imporve test code by reusing common blocks

log "Creating function, testing for cold start with MinScale 0"
fn0=nodejs-hello-$(date +%N)
fission fn create --name $fn0 --env nodejs --code $ROOT/examples/nodejs/hello.js --minscale 0 --maxscale 4 --executortype newdeploy
#trap "fission fn delete --name $fn0" EXIT

log "Creating route"
fission route create --function $fn0 --url /$fn0 --method GET

log "Waiting for router & newdeploy deployment creation"
sleep 5

log "Doing an HTTP GET on the function's route"
response0=$(curl http://$FISSION_ROUTER/$fn0)

log "Checking for valid response"
echo $response0 | grep -i hello


log "Creating function, testing for warm start with MinScale 1"
fn1=nodejs-hello-$(date +%N)
fission fn create --name $fn1 --env nodejs --code $ROOT/examples/nodejs/hello.js --minscale 1 --maxscale 4 --executortype newdeploy
#trap "fission fn delete --name $fn1" EXIT

log "Creating route"
fission route create --function $fn1 --url /$fn1 --method GET

log "Waiting for router & newdeploy deployment creation"
sleep 5

log "Doing an HTTP GET on the function's route"
response1=$(curl http://$FISSION_ROUTER/$fn1)

log "Checking for valid response"
echo $response1 | grep -i hello

# crappy cleanup, improve this later
kubectl get httptrigger -o name | tail -1 | cut -f2 -d'/' | xargs kubectl delete httptrigger

log "NewDeploy ExecutorType: All done."
