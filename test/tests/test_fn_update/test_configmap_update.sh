#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

source $(dirname $0)/fnupdate_utils.sh

ROOT=$(dirname $0)/../../..

env=python-$TEST_ID
fn_name=hellopy-$TEST_ID

old_cfgmap=old-cfgmap-$TEST_ID
new_cfgmap=new-cfgmap-$TEST_ID

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

sed "s/{{ FN_CFGMAP }}/${old_cfgmap}/g" \
    $(dirname $0)/../test_secret_cfgmap/cfgmap.py.template \
    > $tmp_dir/cfgmap.py

log "Creating env $env"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE

log "Creating configmap $old_cfgmap"
kubectl create configmap ${old_cfgmap} --from-literal=TEST_KEY="TESTVALUE" -n default

log "Creating NewDeploy function spec: $fn_name"
pushd $tmp_dir
fission spec init
fission fn create --spec --name $fn_name --env $env --code cfgmap.py --configmap $old_cfgmap --minscale 1 --maxscale 4 --executortype newdeploy
fission spec apply
popd

log "Creating route"
fission route create --name ${fn_name} --function ${fn_name} --url /${fn_name} --method GET

log "Waiting for router to catch up"
sleep 5

log "Testing function"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE'"

log "Creating a new cfgmap"
kubectl create configmap ${new_cfgmap} --from-literal=TEST_KEY="TESTVALUE_NEW" -n default

log "Updating cfgmap and code for the function"
sed -i "s/${old_cfgmap}/${new_cfgmap}/g" $tmp_dir/cfgmap.py
sed -i "s/${old_cfgmap}/${new_cfgmap}/g" $tmp_dir/specs/function-$fn_name.yaml

log "Applying function changes"
pushd $tmp_dir
fission spec apply
popd

log "Waiting for changes to take effect"
sleep 5

log "Testing function for cfgmap value"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE_NEW'"

log "Test PASSED"
