#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

TEST_NS=ns-$TEST_ID

ROOT=$(dirname $0)/../../..

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    clean_resource_by_id_for_namespace $TEST_ID $TEST_NS
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

kubectl create namespace $TEST_NS

log "Creating httptrigger in namespace provided by flag"
fission  httptrigger create --function $fn_n --url /$httptriggerurl --name $httptrigger --namespace $TEST_NS

log "verify trigger exists"
fission httptrigger list --namespace $TEST_NS | grep $httptrigger

log "Test PASSED"
