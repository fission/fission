#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ROOT=$(dirname $0)/../../..

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

env=ts-$TEST_ID
fn_poolmgr=hello-ts-poolmgr-$TEST_ID
fn_nd=hello-ts-nd-$TEST_ID

cd $ROOT/examples/miscellaneous/tensorflow-serving

log "Creating environment for Tensorflow Serving"
fission env create --name $env --image $TS_RUNTIME_IMAGE --version 2 --period 5

zip -r half_plus_two.zip ./half_plus_two

pkgName=$(generate_test_id)
fission package create --name $pkgName --deploy half_plus_two.zip --env $env

# wait for build to finish at most 90s
timeout 90 bash -c "waitBuild $pkgName"

log "Creating pool manager & new deployment function for Tensorflow Serving"
fission fn create --name $fn_poolmgr --env $env --pkg $pkgName --entrypoint "half_plus_two"
fission fn create --name $fn_nd      --env $env --pkg $pkgName --entrypoint "half_plus_two" --executortype newdeploy

log "Creating route for new deployment function"
fission route create --function $fn_poolmgr --url /$fn_poolmgr --method POST
fission route create --function $fn_nd      --url /$fn_nd      --method POST

log "Waiting for router & pools to catch up"
sleep 5

body='{\"instances\": [1.0, 2.0, 5.0]}'
expect='\"predictions\": \[2.5, 3.0, 4.5'

log "Testing pool manager function"
timeout 60 bash -c "test_post_route $fn_poolmgr \"$body\" \"$expect\""

log "Testing new deployment function"
timeout 60 bash -c "test_post_route $fn_nd \"$body\" \"$expect\""

log "Test PASSED"
