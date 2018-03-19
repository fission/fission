#!/bin/bash

set -euo pipefail

source $(dirname $0)/fnupdate_utils.sh

ROOT=$(dirname $0)/../../..

env=python-$(date +%N)
fn_secret=hellosecret-$(date +%N)
fn_config=helloconfig-$(date +%N)

cp ../test_secret_cfgmap/secret.py.template secret.py
sed -i "s/{{ FN_SECRET }}/${fn_secret}/g" secret.py

cp ../test_secret_cfgmap/cfgmap.py.template cfgmap.py
sed -i "s/{{ FN_CFGMAP }}/${fn_config}/g" cfgmap.py

log "Creating env $env"
fission env create --name $env --image fission/python-env
trap "fission env delete --name $env" EXIT

log "Creating NewDeploy function $fn_secret"
fission fn create --name ${fn_secret} --env $env --code $ROOT/examples/python/hello.py --minscale 1 --maxscale 4 --executortype newdeploy
trap "fission fn delete --name ${fn_secret}" EXIT

log "Creating route"
fission route create --function ${fn_secret} --url /${fn_secret} --method GET

log "Waiting for router to catch up"
sleep 5

log "Testing function"
timeout 60 bash -c "test_fn $fn_secret 'world'"

log "Creating secret"
kubectl create secret generic ${fn_secret} --from-literal=TEST_KEY="TESTVALUE" -n default
trap "kubectl delete secret ${fn_secret} -n default" EXIT

log "Updating secret and code for the function"
fission fn update --name ${fn_secret} --env $env --code secret.py --secret ${fn_secret} --minscale 1 --maxscale 4 --executortype newdeploy

log "Waiting for changes to take effect"
sleep 5

log "Testing function for secret value"
timeout 60 bash -c "test_fn $fn_secret 'TESTVALUE'"


log "Creating configmap"
kubectl create configmap ${fn_cfgmap} --from-literal=TEST_KEY="TESTVALUE" -n default
trap "kubectl delete configmap ${fn_cfgmap} -n default" EXIT

log "Updating configmap and code for the function"
fission fn update --name ${fn_secret} --env $env --code cfgmap.py --configmap ${fn_cfgmap} --minscale 1 --maxscale 4 --executortype newdeploy

log "Waiting for changes to take effect"
sleep 5

log "Testing function for secret value"
timeout 60 bash -c "test_fn $fn_secret 'TESTVALUE'"