#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

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

# init
fission spec init

# verify init
[ -d specs ]
[ -f specs/README ]
[ -f specs/fission-deployment-config.yaml ]

log "Apply specs"
fission spec apply

sleep 5

log "verify deployarchive function works"
fission fn test --name deployarchive

log "verify sourcearchive function works"
fission fn test --name sourcearchive

log "Test PASSED"
