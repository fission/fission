#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..

fn=nodejs-hello-$(date +%N)

# Create a hello world function in nodejs, test it with an http trigger

echo "Test test, please ignore."

fission env create --name nodejs --image fission/node-env
trap "fission env delete --name nodejs" EXIT

fission fn create --name $fn --env nodejs --code $ROOT/examples/nodejs/hello.js
trap "fission fn delete --name $fn" EXIT

fission route create --function $fn --url /$fn --method GET

sleep 3

response=$(curl http://$FISSION_ROUTER/$fn)
echo $response | grep -i hello
