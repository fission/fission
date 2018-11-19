#!/bin/bash

set -euo pipefail

fn=spec-$(date +%N)
env=python-$fn

# init
fission spec init

# verify init
[ -d specs ]
[ -f specs/README ]
[ -f specs/fission-deployment-config.yaml ]

fission env create --spec --name $env --image fission/python-env

log "create env spec"
fission spec apply
trap "fission spec destroy" EXIT

log "verify env created"
fission env list | grep python

log "generate function spec"
fission fn create --spec --name $fn --env $env --code hello.py


[ -f specs/function-$fn.yaml ]
grep ArchiveUploadSpec specs/*.yaml
grep Package specs/*.yaml
grep Function specs/*.yaml

log "Apply specs"
fission spec apply
trap "fission fn delete --name $fn" EXIT

log "verify function exists"
fission fn list | grep $fn

sleep 3

log "Test the function"
fission fn test --name $fn | grep -i hello