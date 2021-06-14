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

DIR=$(dirname $0)

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating websocket setup.."
fission spec apply --specdir=$DIR/specs

log "Testing websocket connection"
cd $DIR && go run main.go
