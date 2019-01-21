#!/bin/bash

set -euo pipefail

# Create a function with source package in python 
# to test builder manger functionality. 
# There are two ways to trigger the build
# 1. manually trigger by http post 
# 2. package watcher triggers the build if any changes to packages

ROOT=$(dirname $0)/../..
PYTHON_RUNTIME_IMAGE=${PYTHON_RUNTIME_IMAGE:-gcr.io/fission-ci/python-env:test}
PYTHON_BUILDER_IMAGE=${PYTHON_BUILDER_IMAGE:-gcr.io/fission-ci/python-env-builder:test}

fn=python-srcbuild-$(date +%s)

checkFunctionResponse() {
    log "Doing an HTTP GET on the function's route"
    response=$(curl http://$FISSION_ROUTER/$1)

    log "Checking for valid response"
    log $response
    echo $response | grep -i "a: 1 b: {c: 3, d: 4}"
}

waitBuild() {
    log "Waiting for builder manager to finish the build"
    
    while true; do
      kubectl --namespace default get packages $1 -o jsonpath='{.status.buildstatus}'|grep succeeded
      if [[ $? -eq 0 ]]; then
          break
      fi
    done
}
export -f waitBuild

waitEnvBuilder() {
    env=$1
    envRV=$(kubectl -n default get environments ${env} -o jsonpath='{.metadata.resourceVersion}')

    log "Waiting for env builder to catch up"

    while true; do
      kubectl -n fission-builder get pod -l envName=${env},envResourceVersion=${envRV} \
        -o jsonpath='{range .items[*]}{@.metadata.name}:{range @.status.conditions[*]}{@.type}={@.status};{end}{end}' | grep "Ready=True" | grep -i "$1"
      if [[ $? -eq 0 ]]; then
          break
      fi
    done
}
export -f waitEnvBuilder

cleanup() {
    log "Cleaning up..."
    fission env delete --name python || true
    fission fn delete --name $fn || true
    rm demo-src-pkg.zip || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Pre-test cleanup"
fission env delete --name python || true
kubectl --namespace default get packages|grep -v NAME|awk '{print $1}'|xargs -I@ bash -c 'kubectl --namespace default delete packages @' || true

log "Creating python env"
fission env create --name python --image $PYTHON_RUNTIME_IMAGE --builder $PYTHON_BUILDER_IMAGE

timeout 180s bash -c "waitEnvBuilder python"

log "Creating source pacakage"
zip -jr demo-src-pkg.zip $ROOT/examples/python/sourcepkg/

log "Creating function " $fn
fission fn create --name $fn --env python --src demo-src-pkg.zip --entrypoint "user.main" --buildcmd "./build.sh"

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 3

pkg=$(kubectl --namespace default get functions $fn -o jsonpath='{.spec.package.packageref.name}')

# wait for build to finish at most 60s
timeout 60s bash -c "waitBuild $pkg"

checkFunctionResponse $fn

log "Updating function " $fn
fission fn update --name $fn --src demo-src-pkg.zip

pkg=$(kubectl --namespace default get functions $fn -o jsonpath='{.spec.package.packageref.name}')

# wait for build to finish at most 60s
timeout 60s bash -c "waitBuild $pkg"

checkFunctionResponse $fn

# crappy cleanup, improve this later
kubectl get httptrigger -o name | tail -1 | cut -f2 -d'/' | xargs kubectl delete httptrigger

log "All done."
