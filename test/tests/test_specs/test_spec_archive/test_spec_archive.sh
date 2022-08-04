#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../../utils.sh
ROOT=` realpath $(dirname $0)/../../../../`

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    fission spec destroy
    rm -rf func
    popd
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

pushd $(dirname $0)

[ -d specs ]
[ -f specs/README ]
[ -f specs/fission-deployment-config.yaml ]

mkdir -p func
cp $ROOT/examples/nodejs/hello.js func/deploy.js
cp $ROOT/examples/nodejs/hello.js func/source.js

fission spec destroy || true

log "Apply specs"
fission --verbosity 2 spec apply

log "verify deployarchive function works"
fission fn test --name deployarchive

timeout 60s bash -c "waitBuild sourcearchive"

log "verify sourcearchive function works"
fission fn test --name sourcearchive

log "Test PASSED"
