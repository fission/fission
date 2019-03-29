#!/bin/bash

set -euo pipefail

source $(dirname $0)/fnupdate_utils.sh

ROOT=$(dirname $0)/../../..

env=python-$(date +%N)
fn_name=hellopython-$(date +%N)

old_secret=old-secret-$(date +%N)
new_secret=new-secret-$(date +%N)

cleanup() {
    log "Cleaning up..."
    fission env delete --name $env || true
    kubectl delete secret ${old_secret} -n default || true
    kubectl delete secret ${new_secret} -n default || true
    fission spec destroy || true
    rm -rf specs || true
    rm secret.py || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

cp $ROOT/test/tests/test_secret_cfgmap/secret.py.template secret.py
sed -i "s/{{ FN_SECRET }}/${old_secret}/g" secret.py

log "Creating env $env"
fission env create --name $env --image fission/python-env

log "Creating secret $old_secret"
kubectl create secret generic ${old_secret} --from-literal=TEST_KEY="TESTVALUE" -n default

log "Creating NewDeploy function spec: $fn_name"
fission spec init
fission fn create --spec --name $fn_name --env $env --code secret.py --secret $old_secret --minscale 1 --maxscale 4 --executortype newdeploy
fission spec apply ./specs/

log "Creating route"
fission route create --function ${fn_name} --url /${fn_name} --method GET

log "Waiting for router to catch up"
sleep 5

log "Testing function"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE'"

log "Creating a new secret"
kubectl create secret generic ${new_secret} --from-literal=TEST_KEY="TESTVALUE_NEW" -n default

log "Updating secret and code for the function"
sed -i "s/${old_secret}/${new_secret}/g" secret.py
sed -i "s/${old_secret}/${new_secret}/g" specs/function-$fn_name.yaml

log "Applying function changes"
fission spec apply ./specs/

log "Waiting for changes to take effect"
sleep 5

log "Testing function for secret value"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE_NEW'"
