#!/bin/bash

DEMO_RUN_FAST=1
ROOT_DIR=$(dirname $0)/..
. $ROOT_DIR/util.sh

#
# Canary Deployments
#

desc "Add the NodeJS environment to fission"
run "fission env create --name nodejs --image fission/node-env"

# set up functions
fission fn create --name func-v1 --env nodejs --code func-v1.js
desc "Function version 1"
run "fission function get --name func-v1"

fission fn create --name func-v3 --env nodejs --code func-v3.js
desc "Function version 3"
run "fission function get --name func-v3"

desc "Create a route \(HTTP trigger\) the version-1 of the function with weight 50% and version-3 with weight 50%"
run "fission route create --name route-canary --method GET --url /canary --function func-v3 --weight 50 --function func-v1 --weight 50"

desc "Start sending requests to the route"
run_bg "hey -n 1000000 -c 1 -q 200 http://$FISSION_ROUTER/canary"

sleep 60

desc "Create a canary config: with an increment of 10 percent, every 1 minute, rolling back if 10% of requests fail"
run "fission canary-config create --name canary-1 --funcN func-v3 --funcN-1 func-v1 --httptrigger route-canary --increment-step 10 --increment-interval 60s --failure-threshold 10"
