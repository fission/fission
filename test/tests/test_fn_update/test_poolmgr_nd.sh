#!/bin/bash

set -euo pipefail

source $(dirname $0)/fnupdate_utils.sh

ROOT=$(dirname $0)/../../..

env=python-$(date +%N)
fn=hellopython-$(date +%N)

cleanup() {
    log "Cleaning up..."
    fission env delete --name $env || true
    fission fn delete --name $fn || true
}

cleanup
if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating Python env $env"
fission env create --name $env --image fission/python-env --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256
#trap "fission env delete --name $env" EXIT

log "Creating function $fn"
fission fn create --name $fn --env $env --code $ROOT/examples/python/hello.py

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 5

timeout 60 bash -c "test_fn $fn 'world'"

log "Updating function $fn executor type to new deployment"
fission fn update --name $fn --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy

log "Waiting for router to catch up"
sleep 5

timeout 60 bash -c "test_fn $fn 'world'"

log "Updating function $fn executor type back to pool manager"
fission fn update --name $fn --code $ROOT/examples/python/hello.py --executortype poolmgr

log "Waiting for router to catch up"
sleep 5

timeout 60 bash -c "test_fn $fn 'world'"
