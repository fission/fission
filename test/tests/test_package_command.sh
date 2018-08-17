#!/bin/bash

set -euo pipefail

# Use package command to create two packages one with source
# archive and the other with deploy archive. Also, create a 
# function to test the packages created by package command are 
# able to work.

ROOT=$(dirname $0)/../..
PYTHON_RUNTIME_IMAGE=gcr.io/fission-ci/python-env:test
PYTHON_BUILDER_IMAGE=gcr.io/fission-ci/python-env-builder:test

fn=python-srcbuild-$(date +%s)

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

checkFunctionResponse() {
    log "Doing an HTTP GET on the function's route"
    response=$(curl http://$FISSION_ROUTER/$1)

    log "Checking for valid response"
    log $response
    echo $response | grep -i "$2"
}

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
}

cleanup
if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Pre-test cleanup"
fission env delete --name python || true

log "Creating python env"
fission env create --name python --image $PYTHON_RUNTIME_IMAGE --builder $PYTHON_BUILDER_IMAGE

timeout 180s bash -c "waitEnvBuilder python"

log "Creating pacakage with source archive"
zip -jr demo-src-pkg.zip $ROOT/examples/python/sourcepkg/
pkgName=$(fission package create --src demo-src-pkg.zip --env python --buildcmd "./build.sh"| cut -f2 -d' '| tr -d \')

# wait for build to finish at most 60s
timeout 60s bash -c "waitBuild $pkgName"

log "Creating function " $fn
fission fn create --name $fn --pkg $pkgName --entrypoint "user.main"

log "Creating route"
fission route create --function $fn --url /$fn --method GET

log "Waiting for router to catch up"
sleep 3
  
checkFunctionResponse $fn 'a: 1 b: {c: 3, d: 4}'

log "Creating package with deploy archive"
mkdir testDir
touch testDir/__init__.py
printf 'def main():\n    return "Hello, world!"' > testDir/hello.py
zip -jr demo-deploy-pkg.zip testDir/
pkgName=$(fission package create --deploy demo-deploy-pkg.zip --env python| cut -f2 -d' '| tr -d \')

log "Updating function " $fn
fission fn update --name $fn --pkg $pkgName --entrypoint "hello.main"

log "Waiting for router to update cache"
sleep 3

checkFunctionResponse $fn 'Hello, world!'

# crappy cleanup, improve this later
kubectl get httptrigger -o name | tail -1 | cut -f2 -d'/' | xargs kubectl delete httptrigger

log "All done."
