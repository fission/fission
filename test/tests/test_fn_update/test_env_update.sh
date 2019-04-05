#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

source $(dirname $0)/fnupdate_utils.sh

ROOT=$(dirname $0)/../../..

env_old=python-old-$TEST_ID
env_new=python-new-$TEST_ID
fn=hellopy-$TEST_ID

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating env $env_old"
fission env create --name $env_old --image $PYTHON_RUNTIME_IMAGE

log "Creating function $fn"
fission fn create --name $fn --env $env_old --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256

log "Creating route for function $fn"
fission route create --function ${fn} --url /${fn} --method GET

log "Waiting for router to catch up"
sleep 5

timeout 60 bash -c "test_fn $fn 'world'"

log "Creating a new env $env_new"
fission env create --name $env_new --image $PYTHON_RUNTIME_IMAGE

log "Updating function with a new environment"
fission fn update --name $fn --env $env_new --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256

log "Waiting for update to catch up"
sleep 5

timeout 60 bash -c "test_fn $fn 'world'"

log "Test PASSED"
