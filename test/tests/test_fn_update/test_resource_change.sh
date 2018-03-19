#!/bin/bash

set -euo pipefail

source $(dirname $0)/fnupdate_utils.sh

ROOT=$(dirname $0)/../../..

env=python-$(date +%N)
fn=hellopython-$(date +%N)

mincpu1=40
maxcpu1=140
minmem1=256
maxmem1=512

mincpu2=80
maxcpu2=200
minmem2=512
maxmem2=768

log "Creating Python env $env"
fission env create --name $env --image fission/python-env --mincpu 20 --maxcpu 100 --minmemory 128 --maxmemory 256
trap "fission env delete --name $env" EXIT

log "Creating function $fn"
fission fn create --name $fn --env $env --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy --mincpu $mincpu1 --maxcpu $maxcpu1 --minmemory $minmem1 --maxmemory $maxmem1

maxcpu_actual=$(kubectl describe function $fn -n default|grep Cpu|head -1|cut -f 2|tr -dc '0-9')

mincpu_actual = $(kubectl describe function $fn -n default|grep Cpu|tail -1|cut -f 2|tr -dc '0-9')