#!/bin/bash

#test:disabled

set -euo pipefail

fn=spec-$(date +%N)
env=python-$fn

# init
fission spec init

# verify init
[ -d specs ]
[ -f specs/README ]
[ -f specs/fission-deployment-config.yaml ]

# TODO replace with `fission env create --spec`
fission env create --spec --name $env --image fission/python-env:0.4.0

# create env
fission spec apply
trap "fission env delete --name $env" EXIT

# verify env created
fission env list | grep python

# generate function spec
fission fn create --spec --name $fn --env $env --code $(dirname $0)/hello.py

# verify that function spec exists and has ArchiveUploadSpec, Package and Function
[ -f specs/function-$fn.yaml ]
grep ArchiveUploadSpec specs/*.yaml
grep Package specs/*.yaml
grep Function specs/*.yaml

# create function
fission spec apply
trap "fission fn delete --name $fn" EXIT

# verify function
fission fn list | grep $fn

sleep 3

fission fn test --name $fn | grep -i hello

fission spec destroy