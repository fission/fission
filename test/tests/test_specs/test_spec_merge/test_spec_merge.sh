#!/bin/bash

set -euo pipefail

env_p=nodep
fn_p=nodehellop
env_n=nodend
fn_nd=nodehellond

cleanup() {
    echo "Cleaning up..."
    popd
    fission spec destroy || true
}

trap cleanup EXIT

pushd $(dirname $0)

fission spec apply

fission fn test --name $fn_p

fission fn test --name $fn_nd

hnd=$(kubectl -n $FUNCTION_NAMESPACE get deployment -l=functionName=$fn_nd -ojsonpath='{.items[0].spec.template.spec.hostname}')

if [[ "${hnd}" == "foo-bar" ]]
    then
        echo "Hostname matches for newdeployment function, podspec test 1/2 passsed"
    fi

hnp=$(kubectl -n $FUNCTION_NAMESPACE get deployment -l=environmentName=$env_p -ojsonpath='{.items[0].spec.template.spec.hostname}')

if [[ "${hnp}" == "foo-bar" ]]
    then
        echo "Hostname matches for poolmgr function, podspec test 2/2 passsed"
    fi    