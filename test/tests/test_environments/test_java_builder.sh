#!/bin/bash

#test:disabled

set -euo pipefail
source $(dirname $0)/../../utils.sh

ROOT=$(dirname $0)/../../..

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

env=java-$TEST_ID
fn_p=pbuilderhello-$TEST_ID
fn_n=nbuilderhello-$TEST_ID

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

cd $ROOT/examples/java/hello-world

log "Creating zip from source code"
zip -r $tmp_dir/java-src-pkg.zip *

log "Creating Java environment with Java Builder"
fission env create --name $env --image $JVM_RUNTIME_IMAGE --version 2 --keeparchive --builder $JVM_BUILDER_IMAGE

timeout 90 bash -c "wait_for_builder $env"

log "Creating package from the source archive"
pkg_name=$(generate_test_id)
fission package create --name $pkg_name --sourcearchive $tmp_dir/java-src-pkg.zip --env $env
log "Created package $pkg_name"

log "Checking the status of package"
timeout 400 bash -c "waitBuild $pkg_name"

log "Creating pool manager & new deployment function for Java"
fission fn create --name $fn_n --pkg $pkg_name --env $env --entrypoint io.fission.HelloWorld --executortype newdeploy --minscale 1 --maxscale 1
fission fn create --name $fn_p --pkg $pkg_name --env $env --entrypoint io.fission.HelloWorld

log "Creating route for pool manager function"
fission route create --function $fn_p --url /$fn_p --method GET

log "Creating route for new deployment function"
fission route create --function $fn_n --url /$fn_n --method GET

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function"
timeout 60 bash -c "test_fn $fn_p 'Hello'"

log "Testing new deployment function"
timeout 60 bash -c "test_fn $fn_n 'Hello'"

log "Test PASSED"
