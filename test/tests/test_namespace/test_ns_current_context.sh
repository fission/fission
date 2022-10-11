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

httptrigger=http-$TEST_ID
httptriggerurl=/url-$TEST_ID
fn_n=nbuilderhello-$TEST_ID

cd $ROOT/examples/go/hello-world

log "Creating httptrigger using default namespace"
fission  httptrigger create --function $fn_n --url /$httptriggerurl --name $httptrigger

log "verify trigger exists"
fission httptrigger list --namespace default | grep $httptrigger

log "Test PASSED"
