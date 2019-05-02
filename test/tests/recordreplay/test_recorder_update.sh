#!/bin/bash

#
# Simple test of recorder updates
# 0) Setup: One recorder created for function with trigger
# 1) Recorder disabled, cURL request made, check not saved.
# 2) Recorder re-enabled, request repeated, check now saved.
# 3) New trigger created for same function recorded w/ different url, recorder updated to observe requests for this trigger
#    Request repeated at new url, check saved.
#

set -euo pipefail
set +x
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

ROOT=$(dirname $0)/../../..
DIR=$(dirname $0)

env=python-$TEST_ID
fn=rv-$TEST_ID
recName=rec-$TEST_ID

echo "Creating python env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE

echo "Creating function"
fission fn create --name $fn --env $env --code $DIR/rendezvous.py --method GET

echo "Creating http trigger"
generated=$(fission route create --function $fn --method GET --url /$fn | awk '{print $2}'| tr -d "'")

# Wait until trigger is created
sleep 5

echo "Creating recorder"
fission recorder create --name $recName --function $fn
fission recorder get --name $recName

# Wait until recorder is created
sleep 5

# Disable recorder
fission recorder update --name $recName --disable
sleep 5

echo "Issuing cURL request that should not be recorded:"
resp=$(curl -X GET "http://$FISSION_ROUTER/$fn?time=9&date=Tuesday")
set +o pipefail
recordedStatus="$(fission records view --from 5s --to 0s -v | grep $fn | awk '{print $4$5}')"
set -o pipefail
expectedR="We'll meet at 9 on Tuesday."
expectedS=""

if [ "$resp" != "$expectedR" ] || [ "$recordedStatus" != "$expectedS" ]; then
    echo "Response is not equal to expected response."
    log "expected: status = '$expectedS'  resp = '$expectedR'"
    log "result:   status = '$recordedStatus'  resp = '$resp'"
    exit 1
fi

echo "Test case 1) Passed."

# Reenable recorder
fission recorder update --name $recName --enable
sleep 5

echo "Issuing cURL request that should be recorded:"
resp=$(curl -X GET "http://$FISSION_ROUTER/$fn?time=9&date=Tuesday")
expectedR="We'll meet at 9 on Tuesday."
set +o pipefail
recordedStatus="$(fission records view --from 5s --to 0s -v | grep $fn | awk '{print $4$5}')"
set -o pipefail
expectedS="200OK"

if [ "$resp" != "$expectedR" ] || [ "$recordedStatus" != "$expectedS" ]; then
    echo "Response is not equal to expected response."
    log "expected: status = '$expectedS'  resp = '$expectedR'"
    log "result:   status = '$recordedStatus'  resp = '$resp'"
    exit 1
fi

echo "Test case 2) Passed."

# Create new trigger for same function recorded w/ different url
generated2=$(fission route create --function $fn --method GET --url $fn-2 | awk '{print $2}'| tr -d "'")
echo "New trigger: $generated2"

# Update recorder to observe new trigger
fission recorder update --name $recName --trigger $generated2
fission recorder list

echo "Issuing cURL request that should be recorded:"
resp=$(curl -X GET "http://$FISSION_ROUTER/$fn-2?time=9&date=Tuesday")
expectedR="We'll meet at 9 on Tuesday."
set +o pipefail
recordedStatus="$(fission records view --from 5s --to 0s -v | grep $generated2 | awk '{print $4$5}')"
set -o pipefail
expectedS="200OK"

if [ "$resp" != "$expectedR" ] || [ "$recordedStatus" != "$expectedS" ]; then
    echo "Response is not equal to expected response."
    log "expected: status = '$expectedS'  resp = '$expectedR'"
    log "result:   status = '$recordedStatus'  resp = '$resp'"
    exit 1
fi

echo "Test case 3) Passed."

echo "All passed."
exit 0
