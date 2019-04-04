#!/bin/bash

#
# Create two functions, make sure their internal http triggers invoke
# them correctly.
#

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ROOT=$(dirname $0)/../..

env=nodejs-$TEST_ID
f1=f1-$TEST_ID
f2=f2-$TEST_ID
log $f1 $f2

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

log "Creating nodejs env"
fission env create --name $env --image $NODE_RUNTIME_IMAGE



for f in $f1 $f2
do
    echo "module.exports = function(context, callback) { callback(200, \"$f\n\"); }" > $tmp_dir/$f.js
done

log "Creating functions"
for f in $f1 $f2
do
    fission fn create --name $f --env $env --code $tmp_dir/$f.js
done

log "Waiting for router to catch up"
sleep 2

log "Testing internal routes"
for f in $f1 $f2
do
    response=$(curl http://$FISSION_ROUTER/fission-function/$f)
    echo $response | grep $f
done

log "All done."
