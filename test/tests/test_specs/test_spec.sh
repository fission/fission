#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ROOT=$(dirname $0)/../../..

env=python-$TEST_ID
fn=spec-$TEST_ID

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

cp $ROOT/test/tests/test_specs/hello.py $tmp_dir
pushd $tmp_dir

# init
fission spec init

# verify init
[ -d specs ]
[ -f specs/README ]
[ -f specs/fission-deployment-config.yaml ]

fission env create --spec --name $env --image $PYTHON_RUNTIME_IMAGE

log "create env spec"
fission spec apply

log "verify env created"
fission env list | grep $env

log "generate function spec"
fission fn create --spec --name $fn --env $env --code hello.py


[ -f specs/function-$fn.yaml ]
grep ArchiveUploadSpec specs/*.yaml
grep Package specs/*.yaml
grep Function specs/*.yaml

log "Apply specs"
fission spec apply

log "verify function exists"
fission fn list | grep $fn

sleep 3

log "Test the function"
fission fn test --name $fn | grep -i hello

log "Test PASSED"
