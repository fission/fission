#!/bin/bash

#
# Create a function and trigger it using NATS
# 

set -euo pipefail
source $(dirname $0)/../../utils.sh
set +x

log "Creating websocket setup.."
fission spec apply 

log "Testing websocket connection"
go run main.go

log "Deleting websocket setup"
fission spec destroy