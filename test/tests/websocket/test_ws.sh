#!/bin/bash

#
# Create a function and trigger it using NATS
# 

set -euo pipefail
source $(dirname $0)/../../utils.sh

cleanup() {
    log "Deleting websocket setup"
    fission spec destroy 
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating websocket setup.."
fission spec apply --specdir=./test/tests/websocket/specs

log "Testing websocket connection"
cd ./test/tests/websocket/ && go run main.go
