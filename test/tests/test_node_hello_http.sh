#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..

# Create a hello world function in nodejs, test it with an http trigger

echo "Test test, please ignore."

fission env create --name nodejs --image fission/node-env
trap "fission env delete --name nodejs" EXIT

fission fn create --name hello --env nodejs --code $ROOT/examples/nodejs/hello.js
trap "fission fn delete --name hello" EXIT

fission route create --function hello --url /hello --method GET

sleep 3

time curl http://$FISSION_ROUTER/hello

time curl http://$FISSION_ROUTER/hello
