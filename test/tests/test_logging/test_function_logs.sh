#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ROOT=$(dirname $0)/../../..

env=nodejs-$TEST_ID
fn=nodejs-logtest-$TEST_ID

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

# Create a hello world function in nodejs, test it with an http trigger
log "Creating nodejs env"
fission env create --name $env --image $NODE_RUNTIME_IMAGE

log "Creating function"
fission fn create --name $fn --env $env --code $(dirname $0)/log.js

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 3

log "Doing 4 HTTP GETs on the function's route"
for i in 1 2 3 4
do
    curl -s http://$FISSION_ROUTER/$fn
done

log "Grabbing logs, should have 4 calls in logs"

sleep 60

fission function logs --name $fn --detail > $tmp_dir/logfile

size=$(wc -c < $tmp_dir/logfile)
if [ $size == 0 ]
then
    fission function logs --name $fn --detail > $tmp_dir/logfile
fi

log "---function logs---"
cat $tmp_dir/logfile
log "------"
num=$(grep 'log test' $tmp_dir/logfile | wc -l)
log $num logs found

if [ $num -ne 4 ]
then
    log "Test Failed: expected 4, found $num logs"
    exit 1
fi

log "All done."
