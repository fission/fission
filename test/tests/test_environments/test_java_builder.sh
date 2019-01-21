#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../../..

JVM_RUNTIME_IMAGE=${JVM_RUNTIME_IMAGE:-gcr.io/fission-ci/jvm-env:test}
JVM_BUILDER_IMAGE=${JVM_BUILDER_IMAGE:-gcr.io/fission-ci/jvm-env-builder:test}

cleanup() {
    fission fn delete --name pbuilderhello || true
    fission fn delete --name nbuilderhello || true
    fission env delete --name java || true
    rm $ROOT/examples/jvm/java/java-src-pkg.zip || true
}

test_fn() {
    echo "Checking for valid response"

    while true; do
      response0=$(curl http://$FISSION_ROUTER/$1)
      echo $response0 | grep -i $2
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
}

test_pkg() {
    echo "Checking for valid response"

    while true; do
      response0=$(kubectl get -ndefault package $1 -o=jsonpath='{.status.buildstatus}')
      echo $response0 | grep -i $2
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
}

export -f test_fn
export -f test_pkg

cd $ROOT/examples/jvm/java

trap cleanup EXIT

log "Creating zip from source code"
zip -r java-src-pkg.zip *

log "Creating Java environment with Java Builder"
fission env create --name java --image $JVM_RUNTIME_IMAGE --version 2 --keeparchive --builder $JVM_BUILDER_IMAGE

log "Creating package from the source archive"
pkg_name=`fission package create --sourcearchive java-src-pkg.zip --env java|cut -d' ' -f 2|cut -d"'" -f 2`
log "Created package $pkg_name"

log "Checking the status of package"
timeout 300 bash -c "test_pkg $pkg_name 'succeeded'"

log "Creating pool manager & new deployment function for Java"
fission fn create --name nbuilderhello --pkg $pkg_name --env java --entrypoint io.fission.HelloWorld --executortype newdeploy --minscale 1 --maxscale 1
fission fn create --name pbuilderhello --pkg $pkg_name --env java --entrypoint io.fission.HelloWorld

log "Creating route for pool manager function"
fission route create --function pbuilderhello --url /pbuilderhello --method GET

log "Creating route for new deployment function"
fission route create --function nbuilderhello --url /nbuilderhello --method GET

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function"
timeout 60 bash -c "test_fn pbuilderhello 'Hello'"

log "Testing new deployment function"
timeout 60 bash -c "test_fn nbuilderhello 'Hello'"
