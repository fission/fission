#!/bin/bash

set -euo pipefail

source $(dirname $0)/fnupdate_utils.sh

ROOT=$(dirname $0)/../../..

env=python-$(date +%N)
fn_name=hellopy-$(date +%N)

old_cfgmap=old-cfgmap-$(date +%N)
new_cfgmap=new-cfgmap-$(date +%N)

cleanup() {
    log "Cleaning up..."
    fission env delete --name $env || true
    kubectl delete configmap ${old_cfgmap} -n default || true
    kubectl delete configmap ${new_cfgmap} -n default || true
    fission spec destroy || true
    rm -rf specs || true
    rm cfgmap.py || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

cp ../test_secret_cfgmap/cfgmap.py.template cfgmap.py
sed -i "s/{{ FN_CFGMAP }}/${old_cfgmap}/g" cfgmap.py

log "Creating env $env"
fission env create --name $env --image fission/python-env

log "Creating configmap $old_cfgmap"
kubectl create configmap ${old_cfgmap} --from-literal=TEST_KEY="TESTVALUE" -n default

log "Creating NewDeploy function spec: $fn_name"
fission spec init
fission fn create --spec --name $fn_name --env $env --code cfgmap.py --configmap $old_cfgmap --minscale 1 --maxscale 4 --executortype newdeploy
fission spec apply ./specs/

log "Creating route"
fission route create --function ${fn_name} --url /${fn_name} --method GET

log "Waiting for router to catch up"
sleep 5

log "Testing function"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE'"

log "Creating a new cfgmap"
kubectl create configmap ${new_cfgmap} --from-literal=TEST_KEY="TESTVALUE_NEW" -n default

log "Updating cfgmap and code for the function"
sed -i "s/${old_cfgmap}/${new_cfgmap}/g" cfgmap.py
sed -i "s/${old_cfgmap}/${new_cfgmap}/g" specs/function-$fn_name.yaml

log "Applying function changes"
fission spec apply ./specs/

log "Waiting for changes to take effect"
sleep 5

log "Testing function for cfgmap value"
timeout 60 bash -c "test_fn $fn_name 'TESTVALUE_NEW'"
