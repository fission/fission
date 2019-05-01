#!/bin/bash

#
# Simple end-to-end test of record with GET
# 0) Setup: Two triggers created for a function (different urls, urlA and urlB)
# 1) One recorder created for that function, two cURL requests made to both urls, check both are recorded
# 2) One recorder created for a particular trigger (urlB), both requests repeated, check only the one for urlB was recorded
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
expectedR="We'll meet at 9 on Tuesday."

env=python-$TEST_ID
fn=rv-$TEST_ID
recName1=rec1-$TEST_ID
recName2=rec2-$TEST_ID

echo "Creating python env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE

echo "Creating function"
fission fn create --name $fn --env $env --code $DIR/rendezvous.py --method GET

echo "Creating trigger A"
triggerA=$(fission route create --function $fn --method GET --url /$fn-A | awk '{print $2}'| tr -d "'")
log "triggerA = $triggerA"

echo "Creating trigger B"
triggerB=$(fission route create --function $fn --method GET --url /$fn-B | awk '{print $2}'| tr -d "'")
log "triggerB = $triggerB"

# Wait until triggers are created
sleep 5

echo "Creating recorder by function"
fission recorder create --name $recName1 --function $fn
fission recorder get --name $recName1

# Wait until recorder is created
sleep 5

echo "Issuing cURL request to urlA:"
respA=$(curl -X GET "http://$FISSION_ROUTER/$fn-A?time=9&date=Tuesday")
recordedStatusA="$(fission records view --from 5s --to 0s -v | grep $triggerA | awk '{print $4$5}')"
expectedSA="200OK"

# Separate records
sleep 5

echo "Issuing cURL request to urlB:"
respB=$(curl -X GET "http://$FISSION_ROUTER/$fn-B?time=9&date=Tuesday")
recordedStatusB="$(fission records view --from 5s --to 0s -v | grep $triggerB | awk '{print $4$5}')"
expectedSB="200OK"

if [ "$respA" != "$expectedR" ] || [ "$recordedStatusA" != "$expectedSA" ] || [ "$recordedStatusB" != "$expectedSB" ]; then
    echo "Failed at test case 1."
    log "expected: statusA = '$expectedSA'  statusB = '$statusB'   respA = '$expectedR'"
    log "result:   statusA = '$recordedStatusA'  statusB = '$recordedStatusB'   respA = '$respA'"
    exit 1
fi

echo "Test case 1) Passed."

# Delete first recorder
fission recorder delete --name $recName1

sleep 5

echo "Creating recorder by trigger"
fission recorder create --name $recName2 --trigger $triggerB
fission recorder get --name $recName2

echo "Issuing cURL request to urlA:"
respA=$(curl -X GET "http://$FISSION_ROUTER/$fn-A?time=9&date=Tuesday")
# We except there is no records here -> grep will exit 1 -> this script exit 1 because 'pipefail' is set
# Temporary disable 'pipefail' here
set +o pipefail
recordedStatusA="$(fission records view --from 5s --to 0s -v | grep $triggerA | awk '{print $4$5}')"
set -o pipefail
expectedSA=""

# Separate records
sleep 5

echo "Issuing cURL request to urlB:"
respB=$(curl -X GET "http://$FISSION_ROUTER/$fn-B?time=9&date=Tuesday")
recordedStatusB="$(fission records view --from 5s --to 0s -v | grep $triggerB | awk '{print $4$5}')"
expectedSB="200OK"

if [ "$respA" != "$expectedR" ] || [ "$recordedStatusA" != "$expectedSA" ] || [ "$recordedStatusB" != "$expectedSB" ]; then
    echo "Failed at test case 2."
    log "expected: statusA = '$expectedSA'  statusB = '$expectedSB'   respA = '$expectedR'"
    log "result:   statusA = '$recordedStatusA'  statusB = '$recordedStatusB'   respA = '$respA'"
    exit 1
fi

echo "All passed."
exit 0
