#!/bin/bash
#test:disabled
set -euo pipefail
ROOT=$(dirname $0)/../..

fn=nodejs-hello-$(date +%s)

# Create a function in nodejs, test it with an HTTP trigger.
# Update it and check it's output, the output should be 
# different from the previous one.

log "Pre-test cleanup"
fission env delete --name nodejs || true

log "Creating nodejs env"
fission env create --name nodejs --image fission/node-env
trap "fission env delete --name nodejs" EXIT

log "Creating function"
echo 'module.exports = function(context, callback) { callback(200, "foo!\n"); }' > foo.js
fission fn create --name $fn --env nodejs --code foo.js
trap "fission fn delete --name $fn" EXIT

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 10

log "Doing an HTTP GET on the function's route"
response=$(curl http://$FISSION_ROUTER/$fn)

log "Checking for valid response"
echo $response | grep -i foo

# Running a background process to keep access the
# function to emulate real online traffic. The router
# should be able to update cache under this situation.
( watch -n1 curl http://$FISSION_ROUTER/$fn ) > /dev/null 2>&1 &
pid=$!

log "Updating function"
echo 'module.exports = function(context, callback) { callback(200, "bar!\n"); }' > bar.js
fission fn update --name $fn --code bar.js
trap "fission fn delete --name $fn" EXIT

log "Waiting for router to update cache"
sleep 10

log "Doing an HTTP GET on the function's route"
response=$(curl http://$FISSION_ROUTER/$fn)

log "Checking for valid response again"
echo $response | grep -i bar

kill -15 $pid

# crappy cleanup, improve this later
kubectl get httptrigger -o name | tail -1 | cut -f2 -d'/' | xargs kubectl delete httptrigger

log "All done."
