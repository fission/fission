#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ROOT=$(dirname $0)/../..

checkpkgsum() {
    local pkg=$1
    local sum=$2

    pkgsum=$(kubectl -n default get packages ${pkg} -o jsonpath='{.spec.deployment.checksum.sum}')

    if [ "${sum}" != "${pkgsum}" ]; then
        log "have different sha256 checksum: ${sum} vs. ${pkgsum}"
        kubectl -n default get packages ${pkg} -o yaml
        exit 1
    fi
}

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Download test script"

url1="https://raw.githubusercontent.com/fission/examples/main/nodejs/hello.js"
url2="https://raw.githubusercontent.com/fission/examples/main/nodejs/hello-callback.js"

wget ${url1}
sum=$(shasum -a 256 hello.js|cut -d' ' -f 1)

wget ${url2}
sum2=$(shasum -a 256 hello-callback.js|cut -d' ' -f 1)

env="nodejs-${TEST_ID}"

log "Function with file URL"
fn1="fn1-$TEST_ID"
fission env create --name ${env} --image $NODE_RUNTIME_IMAGE --period 5
fission fn create --name ${fn1} --env ${env} --code ${url1}
pkgname=$(kubectl -n default get functions ${fn1} -o jsonpath="{.spec.package.packageref.name}")
checkpkgsum ${pkgname} ${sum}

log "Creating route"
fission route create --name ${fn1} --function ${fn1} --url /${fn1} --method GET
sleep 3

timeout 60 bash -c "test_fn ${fn1} 'hello, world'"

log "Update function with file URL"
fission fn update --name ${fn1} --env ${env} --code ${url2}
pkgname=$(kubectl -n default get functions ${fn1} -o jsonpath="{.spec.package.packageref.name}")
checkpkgsum ${pkgname} ${sum2}

sleep 3

timeout 60 bash -c "test_fn ${fn1} 'Hello, world callback!'"

log "Function with file URL & checksum"
fn2="fn2-$TEST_ID"
fission fn create --name ${fn2} --env ${env} --code ${url1} --deploychecksum ${sum}
pkgname=$(kubectl -n default get functions ${fn2} -o jsonpath="{.spec.package.packageref.name}")
checkpkgsum ${pkgname} ${sum}

log "Creating route"
fission route create --name ${fn2} --function ${fn2} --url /${fn2} --method GET

timeout 60 bash -c "test_fn ${fn2} 'hello, world'"

log "Function with file URL & insecure"
fn3="fn3-$TEST_ID"
fission fn create --name ${fn3} --env ${env} --code ${url1} --insecure
pkgname=$(kubectl -n default get functions ${fn3} -o jsonpath="{.spec.package.packageref.name}")
checkpkgsum ${pkgname} ""

log "Creating route"
fission route create --name ${fn3} --function ${fn3} --url /${fn3} --method GET
sleep 3

timeout 60 bash -c "test_fn ${fn3} 'hello, world'"

pkg1="pkg1-$TEST_ID"
pkg2="pkg2-$TEST_ID"
pkg3="pkg3-$TEST_ID"

fission pkg create --name ${pkg1} --env ${env} --code ${url1}
checkpkgsum ${pkg1} ${sum}
fission pkg update --name ${pkg1} --env ${env} --code ${url2}
checkpkgsum ${pkg1} ${sum2}
fission pkg create --name ${pkg2} --env ${env} --code ${url1} --deploychecksum ${sum}
checkpkgsum ${pkg2} ${sum}
fission pkg create --name ${pkg3} --env ${env} --code ${url1} --insecure
checkpkgsum ${pkg3} ""

log "Test PASSED"

exit 0
