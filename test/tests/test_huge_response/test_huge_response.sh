#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

ROOT=$(dirname $0)/../../..

pushd $ROOT/test/tests/test_huge_response

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf response.json
    popd
}

retryPost() {
    local fn=$1
    log "Send huge JSON request body"

    set +e
    while true; do
        curl -X POST http://$FISSION_ROUTER/$fn \
            -H "Content-Type: application/json" \
            --data-binary "@generated.json" > response.json

        difftext=$(diff generated.json response.json)
        if [ -z "$difftext" ]; then
            break
        else
            echo "Receive truncated body"
            sleep 1
        fi
    done
    set -e
}
export -f retryPost

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

env=go-$TEST_ID
fn_poolmgr=hello-go-poolmgr-$TEST_ID

log "Creating environment for Golang"
fission env create --name $env --image $GO_RUNTIME_IMAGE --builder $GO_BUILDER_IMAGE --period 5

timeout 90 bash -c "wait_for_builder $env"

pkgName=$(generate_test_id)
fission package create --name $pkgName --src hello.go --env $env

# wait for build to finish at most 90s
timeout 90 bash -c "waitBuild $pkgName"

log "Creating function for Golang"
fission fn create --name $fn_poolmgr --env $env --pkg $pkgName --entrypoint Handler

log "Creating route for function"
fission route create --name $fn_poolmgr --function $fn_poolmgr --url /$fn_poolmgr --method POST

log "Waiting for router & pools to catch up"
sleep 5

log "Testing function"
timeout 20 bash -c "retryPost $fn_poolmgr"

log "Test PASSED"
