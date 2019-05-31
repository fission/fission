#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

# Create a function with source package in python 
# to test builder manger functionality. 
# There are two ways to trigger the build
# 1. manually trigger by http post 
# 2. package watcher triggers the build if any changes to packages

ROOT=$(dirname $0)/../..

env=python-$TEST_ID
fn=python-srcbuild-$TEST_ID

checkFunctionResponse() {
    log "Doing an HTTP GET on the function's route"
    response=$(curl http://$FISSION_ROUTER/$1)

    log "Checking for valid response"
    log $response
    echo $response | grep -i "a: 1 b: {c: 3, d: 4}"
}

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating python env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE --builder $PYTHON_BUILDER_IMAGE

timeout 180s bash -c "wait_for_builder $env"

log "Creating source pacakage"
zip -jr $tmp_dir/demo-src-pkg.zip $ROOT/examples/python/sourcepkg/

log "Creating function " $fn
fission fn create --name $fn --env $env --src $tmp_dir/demo-src-pkg.zip --entrypoint "user.main" --buildcmd "./build.sh"

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 3

pkg=$(kubectl --namespace default get functions $fn -o jsonpath='{.spec.package.packageref.name}')

# wait for build to finish at most 60s
timeout 60s bash -c "waitBuild $pkg"

checkFunctionResponse $fn

log "Updating function " $fn
fission fn update --name $fn --src $tmp_dir/demo-src-pkg.zip

pkg=$(kubectl --namespace default get functions $fn -o jsonpath='{.spec.package.packageref.name}')

# wait for build to finish at most 60s
timeout 60s bash -c "waitBuild $pkg"

checkFunctionResponse $fn

log "All done."
