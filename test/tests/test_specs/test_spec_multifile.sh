#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../../..

fn=spec-$(date +%N)
env=python-$fn

cleanup() {
    pushd $ROOT/examples/python
    fission spec destroy
    rm -rf $ROOT/examples/python/specs
}

trap cleanup EXIT

pushd $ROOT/examples/python

fission spec init

log "Creating environment spec"
fission env create --spec --name $env --image fission/python-env --builder fission/python-builder
fission env list | grep python

log "Creating function spec"
fission fn create --spec --name $fn --env $env --deploy "multifile/*" --entrypoint main.main

log "Applying specs"
fission spec apply

log "Checking function's existance"
fission fn list | grep $fn

log "Testing function"
fission fn test --name $fn | grep -i hello

log "Destroying spec objects"
fission spec destroy
popd