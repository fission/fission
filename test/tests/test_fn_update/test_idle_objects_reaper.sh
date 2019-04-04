#!/bin/bash

#test:disabled

set -euo pipefail
source $(dirname $0)/../../utils.sh

source $(dirname $0)/fnupdate_utils.sh

env=python-$(date +%s)
fn=hellopython-$(date +%s)
ROOT=$(dirname $0)/../../..

cleanup() {
    log "Cleaning up..."
    fission fn delete --name ${fn}-nd || true
    fission fn delete --name ${fn}-gpm || true
    fission env delete --name $env || true
}

cleanup
if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating Python env $env"
fission env create --name $env --image fission/python-env --period 5

log "Creating function ${fn}-nd, ${fn}-gpm"
fission fn create --name ${fn}-nd --env $env --code $ROOT/examples/python/hello.py --minscale 0 --maxscale 2 --executortype newdeploy
fission fn create --name ${fn}-gpm --env $env --code $ROOT/examples/python/hello.py

log "Creating route for function $fn"
fission route create --function ${fn}-nd --url /${fn}-nd --method GET
fission route create --function ${fn}-gpm --url /${fn}-gpm --method GET

log "Waiting for update to catch up"
sleep 5

timeout 60 bash -c "test_fn ${fn}-nd 'world'"
timeout 60 bash -c "test_fn ${fn}-gpm 'world'"

log "Waiting for idle pod reaper to recycle resources"
# the LIST_OLD function list fsvc older than 2 mins
# so in worst case, we need to wait for up to 4 mins + some buffer
sleep 260

# The replicas of function deployment should be 0 due to minScale = 0
ndDeployReplicas=$(kubectl -n $FUNCTION_NAMESPACE get deploy -l functionName=${fn}-nd -ojsonpath='{.items[0].spec.replicas}')
if [ "$ndDeployReplicas" -ne "0" ]
then
  log "Failed to reap idle function pod for function ${fn}-nd"
  exit 1
fi

gpmNumberOfPod=$(kubectl -n $FUNCTION_NAMESPACE get pod -l functionName=${fn}-gpm -o name|wc -l)
if [ "$gpmNumberOfPod" -ne "0" ]
then
  log "Failed to reap idle function pod for function ${fn}-gpm"
  exit 1
fi

# The executor will scale the deployment from 0 to minScale.
# If minScale is 0 then scale to 1 instead.
timeout 60 bash -c "test_fn ${fn}-nd 'world'"
timeout 60 bash -c "test_fn ${fn}-gpm 'world'"

# The replicas of function deployment should be scaled to 1 due to minScale is 0
ndDeployReplicas=$(kubectl -n $FUNCTION_NAMESPACE get deploy -l functionName=${fn}-nd -ojsonpath='{.items[0].spec.replicas}')
if [ "$ndDeployReplicas" -ne "1" ]
then
  log "Failed to reap idle function pod for function ${fn}-nd"
  exit 1
fi

gpmNumberOfPod=$(kubectl -n $FUNCTION_NAMESPACE get pod -l functionName=${fn}-gpm -o name|wc -l)
if [ "$gpmNumberOfPod" -ne "1" ]
then
  log "Failed to reap idle function pod for function ${fn}-gpm"
  exit 1
fi
