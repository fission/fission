#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh
ROOT=` realpath $(dirname $0)/../../../`
TEST_ID=$(generate_test_id)

cleanup() {
    log "Cleaning up..."
    fission spec destroy || true
    rm -rf document specs
    rm -rf ${TEST_ID}
    popd
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

pushd $tmp_dir

mkdir -p document
cp $ROOT/examples/nodejs/hello.js document/h1.js
cp $ROOT/examples/nodejs/hello.js document/h2.js

log "Create hidden file"
touch document/.im_invisible

log "Create specs"
fission spec init
fission pkg list

#fission env create --name nodejs --image fission/node-env --period 5 --version 2 --spec
fission pkg create --name nodejs --env nodejs --deploy "document/*" --spec

log "Apply specs"
fission --verbosity 2 spec apply

mkdir ${TEST_ID}
fission pkg getdeploy --name nodejs > ${TEST_ID}/a.zip
unzip ${TEST_ID}/a.zip -d ${TEST_ID}/

log "Check whether hidden file exists"
if [ -f ${TEST_ID}/.im_invisible ];
then
    log "Found hidden file"
    ls -al ${TEST_ID}
    exit 1
fi

log "Check file amount"
fileamount=$(ls -al ${TEST_ID} | grep -v total | wc -l)
if [ ! ${fileamount} -eq 5 ];
then
  log "File amount incorrect, expect 5"
  ls -al ${TEST_ID}
  exit 1
fi

log "Test PASSED"
