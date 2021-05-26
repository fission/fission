#!/bin/bash
set -ex


fission env create --name nodejs --image fission/node-env:latest
curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
fission function create --name hello --env nodejs --code hello.js
sleep 30
fission function test --name hello