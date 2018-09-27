#!/bin/bash

DEMO_RUN_FAST=1
ROOT_DIR=$(dirname $0)/..
. $ROOT_DIR/util.sh

desc "A simple function that takes inputs"
run "cat hi.py"

desc "Set up function"
run "fission function create --name hi-py --env python --code hi.py --entrypoint hi.main"

desc "Set up route"
run "fission route create --function hi-py --url /hi --method GET"

desc "Set up recorder"
run "fission recorder create --name my-recorder --function hi-py"

desc "Run function"
run "curl http://$FISSION_ROUTER/hi?name=world"

desc "View recording"
run "fission records view -v"

requid=$(fission records view -v|tail -1|cut -f1 -d' ')

desc "Replay recording"
run "fission replay --reqUID $requid"
