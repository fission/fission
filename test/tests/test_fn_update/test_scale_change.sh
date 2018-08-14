#!/bin/bash

set -euo pipefail

source $(dirname $0)/fnupdate_utils.sh

env=python-$(date +%N)
fn=hellopython-$(date +%N)
ROOT=$(dirname $0)/../../..

targetMinScale=2
targetMaxScale=6
targetCpuPercent=60

cleanup() {
    log "Cleaning up..."
    fission env delete --name $env || true
    fission fn delete --name $fn || true
}

cleanup
if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating Python env $env"
fission env create --name $env --image fission/python-env --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256
#trap "fission env delete --name $env" EXIT

log "Creating function $fn"
fission fn create --name $fn --env $env --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256

log "Creating route for function $fn"
fission route create --function ${fn} --url /${fn} --method GET

log "Waiting for update to catch up"
sleep 5

timeout 60 bash -c "test_fn $fn 'world'"

log "Updating function scale and target CPU percent for $fn"
fission fn update --name $fn --code $ROOT/examples/python/hello.py --minscale $targetMinScale --maxscale $targetMaxScale --targetcpu $targetCpuPercent --executortype newdeploy --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256

log "Waiting for update to catch up"
sleep 5

#If variable not used, shell assumes 'function' to be a real function
func=function
actualMinScale=$(kubectl -n default get $func $fn -ojsonpath='{.spec.InvokeStrategy.ExecutionStrategy.MinScale}')
actualMaxScale=$(kubectl -n default get $func $fn -ojsonpath='{.spec.InvokeStrategy.ExecutionStrategy.MaxScale}')
actualTargetCPU=$(kubectl -n default get $func $fn -ojsonpath='{.spec.InvokeStrategy.ExecutionStrategy.TargetCPUPercent}')

if [ "$actualMinScale" -ne "$targetMinScale" ]
then
  log "Failed to update min scale for function"
  exit 1
fi

if [ "$actualMaxScale" -ne "$targetMaxScale" ]
then
  log "Failed to update max scale for function"
  exit 1
fi

if [ "$actualTargetCPU" -ne "$targetCpuPercent" ]
then
  log "Failed to update target CPU for the function"
  exit 1
fi

timeout 60 bash -c "test_fn $fn 'world'"
