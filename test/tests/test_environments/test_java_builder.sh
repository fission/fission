#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

ROOT=$(dirname $0)/../../..

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

env=java-$TEST_ID
fn_p=pbuilderhello-$TEST_ID
fn_n=nbuilderhello-$TEST_ID
pkg_list=""

cleanup() {
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
    for pkg in $pkg_list; do
        fission package delete --name $pkg || true
    done
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

test_fn() {
    echo "Checking function for valid response"

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
    echo "Checking package for valid response"

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

log "Creating zip from source code"
zip -r $tmp_dir/java-src-pkg.zip *

log "Creating Java environment with Java Builder"
fission env create --name $env --image $JVM_RUNTIME_IMAGE --version 2 --keeparchive --builder $JVM_BUILDER_IMAGE

log "Creating package from the source archive"
pkg_name=`fission package create --sourcearchive $tmp_dir/java-src-pkg.zip --env $env|cut -d' ' -f 2|cut -d"'" -f 2`
pkg_list="$pkg_list $pkg_name"
log "Created package $pkg_name"

log "Checking the status of package"
timeout 400 bash -c "test_pkg $pkg_name 'succeeded'"

log "Creating pool manager & new deployment function for Java"
fission fn create --name $fn_n --pkg $pkg_name --env $env --entrypoint io.fission.HelloWorld --executortype newdeploy --minscale 1 --maxscale 1
fission fn create --name $fn_p --pkg $pkg_name --env $env --entrypoint io.fission.HelloWorld

log "Creating route for pool manager function"
fission route create --name $fn_p --function $fn_p --url /$fn_p --method GET

log "Creating route for new deployment function"
fission route create --name $fn_n --function $fn_n --url /$fn_n --method GET

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function"
timeout 60 bash -c "test_fn $fn_p 'Hello'"

log "Testing new deployment function"
timeout 60 bash -c "test_fn $fn_n 'Hello'"

log "Test PASSED"
