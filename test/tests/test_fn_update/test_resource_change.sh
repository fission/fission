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

log "Creating route"
fission route create --function ${fn} --url /${fn} --method GET

log "Waiting for updates to take effect"
sleep 5

#If variable not used, shell assumes 'function' to be a real function
func=function

maxcpu_actual=$(kubectl describe $func $fn -n default|grep Cpu|head -1|cut -f 2|tr -dc '0-9')
mincpu_actual=$(kubectl describe $func $fn -n default|grep Cpu|tail -1|cut -f 2|tr -dc '0-9')
maxmem_actual=$(kubectl describe $func $fn -n default|grep Memory|head -1|cut -f 2|tr -dc '0-9')
minmem_actual=$(kubectl describe $func $fn -n default|grep Memory|tail -1|cut -f 2|tr -dc '0-9')

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
fission fn update --name $fn --env $env --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy --mincpu $mincpu2 --maxcpu $maxcpu2 --minmemory $minmem2 --maxmemory $maxmem2

maxcpu_actual=$(kubectl describe $func $fn -n default|grep Cpu|head -1|cut -f 2|tr -dc '0-9')
mincpu_actual=$(kubectl describe $func $fn -n default|grep Cpu|tail -1|cut -f 2|tr -dc '0-9')
maxmem_actual=$(kubectl describe $func $fn -n default|grep Memory|head -1|cut -f 2|tr -dc '0-9')
minmem_actual=$(kubectl describe $func $fn -n default|grep Memory|tail -1|cut -f 2|tr -dc '0-9')

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