#!/bin/bash

set -euo pipefail

# Create a function with source package in python 
# to test builder manger functionality. 
# There are two ways to trigger the build
# 1. manually trigger by http post 
# 2. package watcher triggers the build if any changes to packages

ROOT=$(dirname $0)/../..
PYTHON_RUNTIME_IMAGE=gcr.io/fission-ci/python-env:test
PYTHON_BUILDER_IMAGE=gcr.io/fission-ci/python-env-builder:test

fn=python-srcbuild-$(date +%s)

checkFunctionResponse() {
    echo "Doing an HTTP GET on the function's route"
    response=$(curl http://$FISSION_ROUTER/$1)

    echo "Checking for valid response"
    echo $response
    echo $response | grep -i "a: 1 b: {c: 3, d: 4}"
}

echo "Pre-test cleanup"
fission env delete --name python || true

echo "Creating python env"
fission env create --name python --image $PYTHON_RUNTIME_IMAGE --builder $PYTHON_BUILDER_IMAGE
trap "fission env delete --name python" EXIT

echo "Waiting for env builder to catch up"
sleep 30

echo "Creating source pacakage"
zip -jr demo-src-pkg.zip $ROOT/examples/python/sourcepkg/

echo "Creating function " $fn
fission fn create --name $fn --env python --src demo-src-pkg.zip --entrypoint "main" --buildcmd "./build.sh"
trap "fission fn delete --name $fn" EXIT

echo "Creating route"
fission route create --function $fn --url /$fn --method GET

echo "Waiting for router to catch up"
sleep 3

echo "Doing an HTTP POST on the builder manager's route to start a build"
pkg=$(kubectl --namespace default get functions $fn -o jsonpath='{.spec.package.packageref.name}')
echo $pkg
response=$(curl -X POST $FISSION_URL/proxy/buildermgr/v1/build \
  -H 'content-type: application/json' \
  -d "{\"package\": {\"namespace\": \"default\",\"name\": \"$pkg\"}}")

echo "Waiting for builder manager to finish the build triggered by http request"
sleep 30

checkFunctionResponse $fn

echo "Updating function " $fn
fission fn update --name $fn --src demo-src-pkg.zip
trap "fission fn delete --name $fn" EXIT

echo "Waiting for builder manager to finish the build triggered by packageWatcher"
sleep 30

checkFunctionResponse $fn

# crappy cleanup, improve this later
kubectl get httptrigger -o name | tail -1 | cut -f2 -d'/' | xargs kubectl delete httptrigger

echo "All done."
