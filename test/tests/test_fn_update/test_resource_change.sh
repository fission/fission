#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

source $(dirname $0)/fnupdate_utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

ROOT=$(dirname $0)/../../..

env=python-$TEST_ID
fn=hellopython-$TEST_ID

mincpu1=40
maxcpu1=140
minmem1=256
maxmem1=512

mincpu2=80
maxcpu2=200
minmem2=512
maxmem2=768

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating Python env $env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256

log "Creating function $fn"
fission fn create --name $fn --env $env --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy --mincpu $mincpu1 --maxcpu $maxcpu1 --minmemory $minmem1 --maxmemory $maxmem1

log "Creating route"
fission route create --function ${fn} --url /${fn} --method GET

log "Waiting for updates to take effect"
sleep 5

#If variable not used, shell assumes 'function' to be a real function
func=function

maxcpu_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.limits.cpu}'|tr -dc '0-9')
mincpu_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.requests.cpu}'|tr -dc '0-9')
maxmem_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.limits.memory}'|tr -dc '0-9')
minmem_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.requests.memory}'|tr -dc '0-9')

if [ "$maxcpu_actual" -ne "$maxcpu1" ] || [ "$mincpu_actual" -ne "$mincpu1" ]
then
  log "Failed to override CPU of function from environment defaults"
  exit 1
fi

if [ "$maxmem_actual" -ne "$maxmem1" ] || [ "$minmem_actual" -ne "$minmem1" ]
then
  log "Failed to override Memory of function from environment defaults"
  exit 1
fi

timeout 60 bash -c "test_fn $fn 'world'"

log "Updating function $fn with new resource values"
fission fn update --name $fn --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy --mincpu $mincpu2 --maxcpu $maxcpu2 --minmemory $minmem2 --maxmemory $maxmem2

maxcpu_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.limits.cpu}'|tr -dc '0-9')
mincpu_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.requests.cpu}'|tr -dc '0-9')
maxmem_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.limits.memory}'|tr -dc '0-9')
minmem_actual=$(kubectl get $func $fn -n default -ojsonpath='{.spec.resources.requests.memory}'|tr -dc '0-9')

if [ "$maxcpu_actual" -ne "$maxcpu2" ] || [ "$mincpu_actual" -ne "$mincpu2" ]
then
  log "Failed to update CPU on updated function"
  exit 1
fi

if [ "$maxmem_actual" -ne "$maxmem2" ] || [ "$minmem_actual" -ne "$minmem2" ]
then
  log "Failed to update memory on updated function"
  exit 1
fi

timeout 60 bash -c "test_fn $fn 'world'"
log "Test PASSED"
