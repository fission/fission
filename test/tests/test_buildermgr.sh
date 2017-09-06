#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..
PYTHON_RUNTIME_IMAGE=gcr.io/fission-ci/python-env:test
PYTHON_BUILDER_IMAGE=gcr.io/fission-ci/python-env-builder:test

fn=python-srcbuild-$(date +%s)
buildermgr=$(kubectl get svc/buildermgr -o jsonpath={.status.loadBalancer.ingress[*].ip})

echo "Pre-test cleanup"
fission env delete --name python || true

echo "Creating python env"
fission env create --name python --image $PYTHON_RUNTIME_IMAGE --builder $PYTHON_BUILDER_IMAGE
trap "fission env delete --name python" EXIT

echo "Waiting for env builder to catch up"
sleep 10

echo "Creating function"
fission fn create --name $fn --env python --srcpkg $ROOT/examples/python/sourcepkg/demo-src-pkg.zip --codepath /userfunc/user/user
trap "fission fn delete --name $fn" EXIT

echo "Creating route"
fission route create --function $fn --url /$fn --method GET

echo "Waiting for router to catch up"
sleep 3

echo "Doing an HTTP POST on the builder manager's route to start a build"
response=$(curl -X POST http://$buildermgr/build \
  -H 'content-type: application/json' \
  -d "{\"function\": {\"namespace\": \"default\",\"name\": \"$fn\"}}")

echo "Waiting for builder manager to finish the build"
sleep 10

echo "Doing an HTTP GET on the function's route"
response=$(curl http://$FISSION_ROUTER/$fn)

echo "Checking for valid response"
echo $response | grep -i "a: 1 b: {c: 3, d: 4}"

# crappy cleanup, improve this later
kubectl get httptrigger -o name | tail -1 | cut -f2 -d'/' | xargs kubectl delete httptrigger

echo "All done."
