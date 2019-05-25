#!/bin/bash

#
# Simple end-to-end test of record with POST
# Two recorders tested: by function, by trigger (TODO)
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
fn=greetings-$TEST_ID
recName=rec-$TEST_ID

echo "Creating python env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE

echo "Creating function"
fission fn create --name $fn --env $env --code $DIR/greetings.py --method GET

echo "Creating http trigger"
generated=$(fission route create --function $fn --method POST --url /$fn | awk '{print $2}'| tr -d "'")

# Wait until trigger is created
sleep 5

echo "Creating recorder"
fission recorder create --name $recName --function $fn
fission recorder get --name $recName

# Wait until recorder is created
sleep 5

echo "Issuing cURL request:"
resp=$(curl -X POST "http://$FISSION_ROUTER/$fn" -d "{\"title\":\"Madam\",\"name\":\"Thanh\",\"item\":\"coat\"}")
expectedR="Greetings, Madam Thanh. May I take your coat?"

set +o pipefail
recordedStatus="$(fission records view --from 15s --to 0s -v | grep $fn | awk '{print $4$5}')"
set -o pipefail
expectedS="200OK"

if [ "$resp" != "$expectedR" ] || [ "$recordedStatus" != "$expectedS" ]; then
    echo "Response is not equal to expected response."
    log "expected: status = '$expectedS'  resp = '$expectedR'"
    log "result:   status = '$recordedStatus'  resp = '$resp'"
    exit 1
fi

echo "Passed."
exit 0
