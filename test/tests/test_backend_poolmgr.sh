#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../.. 

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

fn=nodejs-hello-$(date +%N)

# Create a hello world function in nodejs, test it with an http trigger
log "Poolmgr ExecutorType: Pre-test cleanup"
fission env delete --name nodejs || true

log "Creating nodejs env"
fission env create --name nodejs --image fission/node-env --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256
#trap "fission env delete --name nodejs" EXIT

log "Creating function"
fission fn create --name $fn --env nodejs --code $ROOT/examples/nodejs/hello.js --executortype poolmgr
#trap "fission fn delete --name $fn" EXIT

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 5

log "Doing an HTTP GET on the function's route"
response=$(curl http://$FISSION_ROUTER/$fn)

log "Checking for valid response"
echo $response | grep -i hello

# crappy cleanup, improve this later
kubectl get httptrigger -o name | tail -1 | cut -f2 -d'/' | xargs kubectl delete httptrigger

log "Poolmgr ExecutorType: All done."
