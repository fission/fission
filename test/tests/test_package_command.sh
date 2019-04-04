#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

# Use package command to create packages of type:
#     1) Multiple source files from a directory
#     2) Source archive file
# TBD 3) Source file from a HTTP location
#     4) Deployment files from a directory
#     5) Deployment archive
# TBD 6) Deployment archive from a HTTP location
# TBD 7) Multiple files from  multiple directories
# Then create a function to test the packages created by package command are 
# able to work.

# TODO: seperate to multiple tests

ROOT=$(dirname $0)/../..

env=python-$TEST_ID
fn1=python-srcbuild1-$TEST_ID
fn2=python-srcbuild2-$TEST_ID

fn4=python-deploy4-$TEST_ID
fn5=python-deploy5-$TEST_ID

pkg_list=""

waitBuild() {
    log "Waiting for builder manager to finish the build"
    
    while true; do
      kubectl --namespace default get packages $1 -o jsonpath='{.status.buildstatus}'|grep succeeded
      if [[ $? -eq 0 ]]; then
          break
      fi
      log "Waiting for build to finish"
      sleep 1
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
        -o jsonpath='{range .items[*]}{@.metadata.name}:{range @.status.conditions[*]}{@.type}={@.status};{end}{end}' | grep "Ready=True" | grep -i "$env"
      if [[ $? -eq 0 ]]; then
          break
      fi
      sleep 1
    done
}
export -f waitEnvBuilder

cleanup() {
    log "Cleaning up..."
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

log "Creating python env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE --builder $PYTHON_BUILDER_IMAGE

timeout 180s bash -c "waitEnvBuilder $env"
# 1) Multiple source files (multiple inputs, Using * expression, from a directory)
# Currently only * expression implemented as a test
pushd $ROOT/examples/python/
pkg1=$(fission package create --src "sourcepkg/*" --env $env --buildcmd "./build.sh"| cut -f2 -d' '| tr -d \')
pkg_list="$pkg_list $pkg1"
popd
# wait for build to finish at most 60s
timeout 60s bash -c "waitBuild $pkg1"
log "Creating function " $fn1
fission fn create --name $fn1 --pkg $pkg1 --entrypoint "user.main"

log "Creating route"
fission route create --name $fn1 --function $fn1 --url /$fn1 --method GET

log "Waiting for router to catch up"
sleep 3
  
checkFunctionResponse $fn1 'a: 1 b: {c: 3, d: 4}'

# 2) Source archive file
log "Creating pacakage with source archive"
zip -jr $tmp_dir/demo-src-pkg.zip $ROOT/examples/python/sourcepkg/
pkg2=$(fission package create --src $tmp_dir/demo-src-pkg.zip --env $env --buildcmd "./build.sh"| cut -f2 -d' '| tr -d \')
pkg_list="$pkg_list $pkg2"

# wait for build to finish at most 60s
timeout 60s bash -c "waitBuild $pkg2"

log "Creating function " $fn2
fission fn create --name $fn2 --pkg $pkg2 --entrypoint "user.main"

log "Creating route"
fission route create --name $fn2 --function $fn2 --url /$fn2 --method GET

log "Waiting for router to catch up"
sleep 3
  
checkFunctionResponse $fn2 'a: 1 b: {c: 3, d: 4}'

# 3) Source file from a HTTP location
# TBD

# 4) Deployment files from a directory
pushd $ROOT/examples/python/
pkg4=$(fission package create --deploy "multifile/*" --env $env| cut -f2 -d' '| tr -d \')
pkg_list="$pkg_list $pkg4"
popd
log "Creating function " $fn4
fission fn create --name $fn4 --pkg $pkg4 --entrypoint "main.main"

log "Creating route"
fission route create --name $fn4 --function $fn4 --url /$fn4 --method GET

log "Waiting for router to catch up"
sleep 3
  
checkFunctionResponse $fn4 'Hello, world!'

# 5) Deployment archive

log "Creating package with deploy archive"
mkdir $tmp_dir/deploypkg
touch $tmp_dir/deploypkg/__init__.py
printf 'def main():\n    return "Hello, world!"' > $tmp_dir/deploypkg/hello.py
zip -jr $tmp_dir/demo-deploy-pkg.zip $tmp_dir/deploypkg/
pkg5=$(fission package create --deploy $tmp_dir/demo-deploy-pkg.zip --env $env| cut -f2 -d' '| tr -d \')
pkg_list="$pkg_list $pkg5"


log "Updating function " $fn5
fission fn create --name $fn5 --pkg $pkg5 --entrypoint "hello.main"

log "Creating route"
fission route create --name $fn5 --function $fn5 --url /$fn5 --method GET

log "Waiting for router to update cache"
sleep 3

checkFunctionResponse $fn5 'Hello, world!'

# 6) Deployment archive from a HTTP location
# TBD

log "All done."
