#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

source $(dirname $0)/fnupdate_utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ROOT=$(dirname $0)/../../..

env=python-$TEST_ID
fn_name=hellopython-$TEST_ID

old_secret=old-secret-$TEST_ID
new_secret=new-secret-$TEST_ID

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

sed "s/{{ FN_SECRET }}/${old_secret}/g" \
    $ROOT/test/tests/test_secret_cfgmap/secret.py.template \
    > $tmp_dir/secret.py

log "Creating env $env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE

log "Creating secret $old_secret"
kubectl create secret generic ${old_secret} --from-literal=TEST_KEY="TESTVALUE" -n default

log "Creating NewDeploy function spec: $fn_name"
pushd $tmp_dir
fission spec init
fission fn create --spec --name $fn_name --env $env --code secret.py --secret $old_secret --minscale 1 --maxscale 4 --executortype newdeploy
fission spec apply
popd

log "Creating route"
fission route create --function ${fn_name} --url /${fn_name} --method GET

log "Waiting for router to catch up"
sleep 5

log "Testing function"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE'"

log "Creating a new secret"
kubectl create secret generic ${new_secret} --from-literal=TEST_KEY="TESTVALUE_NEW" -n default

log "Updating secret and code for the function"
sed -i "s/${old_secret}/${new_secret}/g" $tmp_dir/secret.py
sed -i "s/${old_secret}/${new_secret}/g" $tmp_dir/specs/function-$fn_name.yaml

log "Applying function changes"
pushd $tmp_dir
fission spec apply
popd

log "Waiting for changes to take effect"
sleep 5

log "Testing function for secret value"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE_NEW'"
log "Test PASSED"
