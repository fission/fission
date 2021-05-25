#!/bin/bash
set -ex

#echo ".................Priting all Pods............................"
kubectl get pods -A
echo ".................installing fission CLI............................"
curl -Lo fission https://github.com/fission/fission/releases/download/1.12.0/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/
echo "....................Showing fission version......................."
fission version
#echo ".................Creating fission function............................"
#fission env create --name nodejs --image fission/node-env:latest
#curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
#fission function create --name hello --env nodejs --code hello.js
#echo "...********************* Testing the Fission function **********************..."
#fission function test --name hello
        