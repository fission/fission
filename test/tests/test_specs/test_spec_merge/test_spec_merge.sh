#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../../utils.sh

env_p=nodep
fn_p=nodehellop
env_n=nodend
fn_nd=nodehellond

cleanup() {
    log "Cleaning up..."
    fission spec destroy || true
    popd
}

trap cleanup EXIT

pushd $(dirname $0)

fission spec apply

log "Waiting for changes to reflect"
sleep 5

fission fn list | grep $fn_p
fission fn test --name $fn_p

fission fn list | grep $fn_nd
fission fn test --name $fn_nd

hnd=$(kubectl -n $FUNCTION_NAMESPACE get deployment -l=functionName=$fn_nd -ojsonpath='{.items[0].spec.template.spec.hostname}')

if [[ "${hnd}" == "foo-bar" ]]; then
    log "Hostname matches for newdeployment function, podspec test 1/2 passsed"
fi

hnp=$(kubectl -n $FUNCTION_NAMESPACE get deployment -l=environmentName=$env_p -ojsonpath='{.items[0].spec.template.spec.hostname}')

if [[ "${hnp}" == "foo-bar" ]]; then
    log "Hostname matches for poolmgr function, podspec test 2/2 passsed"
fi
