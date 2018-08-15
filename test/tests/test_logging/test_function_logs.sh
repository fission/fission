#!/bin/bash


set -euo pipefail

ROOT=$(dirname $0)/../..

fn=nodejs-logtest-$(date +%N)

cleanup() {
    log "Cleaning up..."
    var=$(fission route list | grep $fn | awk '{print $1;}')
    fission fn delete --name $fn || true
    fission env delete --name nodejs || true
    log "delete logfile" || true
    rm "/tmp/logfile" || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

# Create a hello world function in nodejs, test it with an http trigger
log "Creating nodejs env"
fission env create --name nodejs --image fission/node-env
#trap "fission env delete --name nodejs" EXIT

log "Creating function"
fission fn create --name $fn --env nodejs --code log.js
#trap "fission fn delete --name $fn" EXIT

log "Creating route"
fission route create --function $fn --url /$fn --method GET
#trap cleanup EXIT

log "Waiting for router to catch up"
sleep 15

log "Doing 4 HTTP GETs on the function's route"
for i in 1 2 3 4
do
    curl -s http://$FISSION_ROUTER/$fn
done

log "Grabbing logs, should have 4 calls in logs"

sleep 60

fission function logs --name $fn --detail > /tmp/logfile

size=$(wc -c </tmp/logfile)
if [ $size == 0 ]
then
    fission function logs --name $fn --detail > /tmp/logfile
fi

log "---function logs---"
cat /tmp/logfile
log "------"
num=$(grep 'log test' /tmp/logfile | wc -l)
log $num logs found

if [ $num -ne 4 ]
then
    log "Test Failed: expected 4, found $num logs"
    exit 1
fi

log "All done."
