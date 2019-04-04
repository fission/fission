#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

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

# 1. This test first creates a python function with a route
# 2. Makes a curl request to the route and verifies http.StatusOK is received.
#    This step ensures the pod address is cached in router.
# 3. Then, finds the pod that has the function loaded and deletes the pod with grace period 0s.
#    This step results in a stale entry in the router cache.
# 4. Finally, makes a curl request again and waits for response http.StatusOK.
#    This ensures that router invalidated its cache, made a request to executor to get service for function and retried
#    the request against this new address.

env=python-$TEST_ID
fn=python-func-$TEST_ID
route=python-ht-$TEST_ID

log "Creating python env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE

log "Creating hello.py"
printf 'def main():\n    return "Hello, world!"' > $tmp_dir/hello.py

log "Creating function " $fn
fission fn create --name $fn --env $env --code $tmp_dir/hello.py

log "Creating route"
fission route create --name $route --function $fn --url /$fn --method GET

log "Waiting for router to update cache"
sleep 5

http_status=`curl -sw "%{http_code}" http://$FISSION_ROUTER/$fn -o /dev/null`
log "http_status: $http_status"
if [ "$http_status" -ne "200" ]; then
    log "Something went wrong, http status even before deleting function pod is $http_status"
    exit 1
fi

log "getting function pod"
funcPod=`kubectl get pods -n $FUNCTION_NAMESPACE -L functionName | grep $fn| tr -s " "| cut -d" " -f1`
log "funcPod : $funcPod"

kubectl delete pod $funcPod -n $FUNCTION_NAMESPACE --grace-period=0
log  "deleted function pod $funcPod"

http_status=`curl -sw "%{http_code}" http://$FISSION_ROUTER/$fn -o /dev/null`
log "http_status: $http_status"
if [ "$http_status" -ne "200" ]; then
    log "Something went wrong, http status after deleting function pod is $http_status"
    exit 1
fi

log "Test PASSED"
