#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..

fn=spec-$(date +%N)
env=python-$fn

pushd $ROOT/examples/python/multifile
# init
fission spec init

log "Verifying init"
[ -d specs ]
[ -f specs/README ]
[ -f specs/fission-deployment-config.yaml ]

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