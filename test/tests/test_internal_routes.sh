#!/bin/bash

#
# Create two functions, make sure their internal http triggers invoke
# them correctly.
#

set -euo pipefail

ROOT=$(dirname $0)/../..

log "Writing functions"
f1=f1-$(date +%s)
f2=f2-$(date +%s)
log $f1 $f2

cleanup() {
    log "Cleaning up..."
    fission env delete --name nodejs || true
    fission fn delete --name $f1 || true
    fission fn delete --name $f2 || true
    rm $f1.js || true
    rm $f2.js || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Pre-test cleanup"
fission env delete --name nodejs || true

log "Creating nodejs env"
fission env create --name nodejs --image fission/node-env



for f in $f1 $f2
do
    echo "module.exports = function(context, callback) { callback(200, \"$f\n\"); }" > $f.js
done

log "Creating functions"
for f in $f1 $f2
do
    fission fn create --name $f --env nodejs --code $f.js
done

log "Waiting for router to catch up"
sleep 2

log "Testing internal routes"
for f in $f1 $f2
do
    response=$(curl http://$FISSION_ROUTER/fission-function/$f)
    echo $response | grep $f
done

log "All done."
