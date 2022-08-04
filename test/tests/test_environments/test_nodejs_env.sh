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

env_v1api=nodejs-v1-$TEST_ID
env_v2api=nodejs-v2-$TEST_ID
fn1=test-nodejs-env-1-$TEST_ID
fn2=test-nodejs-env-2-$TEST_ID
fn3=test-nodejs-env-3-$TEST_ID
fn4=test-nodejs-env-4-$TEST_ID

test_path=$ROOT/test/tests/test_environments/node_src

log "Creating v1api environment ..."
log "NODE_RUNTIME_IMAGE = $NODE_RUNTIME_IMAGE"
fission env create \
    --name $env_v1api \
    --image $NODE_RUNTIME_IMAGE \

log "Creating v2api environment ..."
log "NODE_RUNTIME_IMAGE = $NODE_RUNTIME_IMAGE     NODE_BUILDER_IMAGE = $NODE_BUILDER_IMAGE"
fission env create \
    --name $env_v2api \
    --image $NODE_RUNTIME_IMAGE \
    --builder $NODE_BUILDER_IMAGE
timeout 180s bash -c "wait_for_builder $env_v2api"


log "===== 1. test env with v1 api ====="
fission fn create --name $fn1 --env $env_v1api --code $test_path/test-case-1/helloWorld.js

fission route create --function $fn1 --url /$fn1 --method GET
sleep 3     # Waiting for router to catch up
timeout 60 bash -c "test_fn $fn1 \"hello, world!\""

log "===== 2. test query string ====="
fission fn create --name $fn2 --env $env_v1api --code $test_path/test-case-2/helloUser.js

fission route create --function $fn2 --url /$fn2 --method GET
sleep 3     # Waiting for router to catch up
timeout 60 bash -c "test_fn $fn2?user=foo \"hello foo!\""

log "===== 3. test POST ====="
fission fn create --name $fn3 --env $env_v1api --code $test_path/test-case-3/wordCount.js

fission route create --function $fn3 --url /$fn3 --method POST
sleep 3     # Waiting for router to catch up
body='Its a beautiful day'
timeout 20 bash -c "test_post_route $fn3 $body 4"

log "===== 4. test builder ====="
log "Creating package ..."
pushd $test_path/test-case-4
zip -r $tmp_dir/src-pkg.zip momentExample.js package.json
popd
pkg=$(generate_test_id)
fission package create --name $pkg --src $tmp_dir/src-pkg.zip --env $env_v2api 
timeout 60s bash -c "waitBuild $pkg"

fission fn create --name $fn4 --pkg $pkg --env $env_v2api --entrypoint "momentExample"

fission route create --function $fn4 --url /$fn4 --method GET
sleep 3     # Waiting for router to catch up
timeout 60 bash -c "test_fn $fn4 'Hello'"

log "Test PASSED"
