#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..

fn=testnormal-$(date +%s)
fn_secret=testsecret-$(date +%s)
fn_cfgmap=testcfgmap-$(date +%s)

cleanup() {
    log "Cleaning up..."
    fission env delete --name python || true
    kubectl delete secret -n default ${fn_secret} || true
    kubectl delete configmap -n default ${fn_cfgmap} || true
    rm cfgmap.py || true
    rm secret.py || true
    # delete functions
    for f in ${fn_secret} ${fn_cfgmap} ${fn}
    do
        fission fn list | grep ${f} | awk '{print $1;}' | xargs -I@ bash -c "fission function delete --name @"
    done
    # delete routes
    for r in ${fn_secret} ${fn_cfgmap} ${fn}
    do
        fission route list | grep ${r} | awk '{print $1;}' | xargs -I@ bash -c "fission route delete --name @"
    done
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

cp secret.py.template secret.py
sed -i "s/{{ FN_SECRET }}/${fn_secret}/g" secret.py

cp cfgmap.py.template cfgmap.py
sed -i "s/{{ FN_CFGMAP }}/${fn_cfgmap}/g" cfgmap.py

checkFunctionResponse() {
    log "Doing an HTTP GET on the function's route"
    val=${2}
    type=${3}

    log "Checking for valid response"
    while true; do
      log curl http://$FISSION_ROUTER/$1
      response0=$(curl http://$FISSION_ROUTER/$1)
      log $response0 | grep -i ${val}
      if [[ $? -eq 0 ]]; then
        log "test ${type} passed"
        break
      fi
      sleep 1
    done
}
export -f checkFunctionResponse

# Create a hello world function in nodejs, test it with an http trigger
log "Pre-test cleanup"
fission env delete --name python || true

log "Creating python env"
fission env create --name python --image fission/python-env

log "Creating secret"
kubectl create secret generic ${fn_secret} --from-literal=TEST_KEY="TESTVALUE" -n default

log "Creating function with secret"
fission fn create --name ${fn_secret} --env python --code secret.py --secret ${fn_secret}

log "Creating route"
fission route create --function ${fn_secret} --url /${fn_secret} --method GET

log "Waiting for router to catch up"
sleep 5

timeout 60 bash -c "checkFunctionResponse ${fn_secret} 'TESTVALUE' 'secret'"

log "Creating function with newdeploy executorType and new secret value"
kubectl patch secrets ${fn_secret} -p '{"data":{"TEST_KEY":"TkVXVkFMCg=="}}' -n default
fission fn create --name ${fn_secret}-1 --env python --code secret.py --secret ${fn_secret} --executortype newdeploy

log "Creating route"
fission route create --function ${fn_secret}-1 --url /${fn_secret}-1 --method GET

log "Waiting for router catch up"
sleep 5

timeout 60 bash -c "checkFunctionResponse ${fn_secret}-1 'NEWVAL' 'secret'"

log "Creating configmap"
kubectl create configmap ${fn_cfgmap} --from-literal=TEST_KEY="TESTVALUE" -n default

log "creating function with configmap"
fission fn create --name ${fn_cfgmap} --env python --code cfgmap.py --configmap ${fn_cfgmap}

log "Creating route"
fission route create --function ${fn_cfgmap} --url /${fn_cfgmap} --method GET

log "Waiting for router to catch up"
sleep 5

timeout 60 bash -c "checkFunctionResponse ${fn_cfgmap} 'TESTVALUE' 'configmap'"

log "Creating function with newdeploy executorType and new configmap value"
kubectl patch configmap ${fn_cfgmap} -p '{"data":{"TEST_KEY":"NEWVAL"}}' -n default
fission fn create --name ${fn_cfgmap}-1 --env python --code cfgmap.py --configmap ${fn_cfgmap} --executortype newdeploy

log "Creating route"
fission route create --function ${fn_cfgmap}-1 --url /${fn_cfgmap}-1 --method GET

log "Waiting for router catch up"
sleep 5

timeout 60 bash -c "checkFunctionResponse ${fn_cfgmap}-1 'NEWVAL' 'configmap'"

log "testing creating a function without a secret or configmap"
fission function create --name ${fn} --env python --code empty.py

log "Creating route"
fission route create --function ${fn} --url /${fn} --method GET

log "Waiting for router to catch up"
sleep 5

log "HTTP GET on the function's route"
timeout 60 bash -c "checkFunctionResponse ${fn} 'yes' 'configmap'"
