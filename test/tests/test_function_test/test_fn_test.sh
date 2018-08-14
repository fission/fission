#!/bin/bash

#test:disabled
# Disabled because CI Fails for invalid function https://github.com/fission/fission/issues/653

set -euo pipefail

env=nodejs-$(date +%N)
valid_fn_name=hello-$(date +%N)
invalid_fn_name=errhello-$(date +%N)

cleanup() {
    log "Cleaning up..."
    fission env delete --name $env || true
    fission fn delete --name $valid_fn_name || true
    fission fn delete --name $invalid_fn_name || true
}

cleanup
if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating env $env"
fission env create --name $env --image fission/node-env

log "Creating valid function $valid_fn_name"
fission fn create --name $valid_fn_name --env $env --code hello.js

log "Testing valid function $valid_fn_name"
fission fn test --name $valid_fn_name > /tmp/valid.log

log "---Valid Function logs---"
cat /tmp/valid.log
log "------"
valid_num=$(grep 'Hello, Fission' /tmp/valid.log | wc -l)

if [ $valid_num -ne 1 ]
then
    log "Valid function Test Failed: expected 1, found $valid_num logs"
    exit 1
fi

log "Creating function with an error $invalid_fn_name"
fission fn create --name $invalid_fn_name --env $env --code errhello.js

log "Testing invalid function $valid_fn_name"
fission fn test --name $invalid_fn_name > /tmp/invalid.log

for i in {1..10}
do
    size=$(wc -c </tmp/invalid.log)
    if [ $size == 0 ]
    then
        fission fn test --name $invalid_fn_name > /tmp/invalid.log
    else
        break
    fi
done

log "---Invalid Function logs---"
cat /tmp/invalid.log
log "------"
invalid_num=$(grep 'SyntaxError' /tmp/invalid.log | wc -l)

if [ $invalid_num -ne 1 ]
then
    log "Invalid function Failed: expected 1, found $invalid_num logs"
    exit 1
fi

log "All tests passed"
