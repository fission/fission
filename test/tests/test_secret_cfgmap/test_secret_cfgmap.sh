#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..

fn=testnormal-$(date +%s)
fn_secret=testsecret-$(date +%s)
fn_cfgmap=testcfgmap-$(date +%s)

cp secret.py.template secret.py
sed -i "s/{{ FN_SECRET }}/${fn_secret}/g" secret.py

cp cfgmap.py.template cfgmap.py
sed -i "s/{{ FN_CFGMAP }}/${fn_cfgmap}/g" cfgmap.py

function cleanup {
    echo_log "Cleanup everything"
    kubectl delete secret -n default ${fn_secret}
    kubectl delete configmap -n default ${fn_cfgmap}
    fission function delete --name ${fn_secret}
    fission function delete --name ${fn_cfgmap}
    fission function delete --name ${fn}
    var=$(fission route list | grep ${fn_secret} | awk '{print $1;}')
    var2=$(fission route list | grep ${fn_cfgmap} | awk '{print $1;}')
    var3=$(fission route list | grep ${fn} | awk '{print $1;}')
    fission route delete --name ${var}
    fission route delete --name ${var2}
    fission route delete --name ${var3}
}

# Create a hello world function in nodejs, test it with an http trigger
echo_log "Pre-test cleanup"
fission env delete --name python || true

echo_log "Creating python env"
fission env create --name python --image fission/python-env
trap "fission env delete --name python" EXIT

echo_log "Creating secret"
kubectl create secret generic ${fn_secret} --from-literal=TEST_KEY="TESTVALUE" -n default
trap "kubectl delete secret ${fn_secret} -n default" EXIT


echo_log "Creating function with secret"
fission fn create --name ${fn_secret} --env python --code secret.py --secret ${fn_secret}
trap "fission fn delete --name ${fn_secret}" EXIT

echo_log "Creating route"
fission route create --function ${fn_secret} --url /${fn_secret} --method GET

echo_log "Waiting for router to catch up"
sleep 5

echo_log "HTTP GET on the function's route"
res=$(curl http://${FISSION_ROUTER}/${fn_secret})
val='TESTVALUE'

if [[ ${res} != ${val} ]]
then
	echo_log "test secret failed"
	cleanup
	exit 1
fi
echo_log "test secret passed"

echo_log "Creating configmap"
kubectl create configmap ${fn_cfgmap} --from-literal=TEST_KEY=TESTVALUE -n default
trap "kubectl delete configmap ${fn_cfgmap} -n default" EXIT

echo_log "creating function with configmap"
fission fn create --name ${fn_cfgmap} --env python --code cfgmap.py --configmap ${fn_cfgmap}
trap "fission fn delete --name ${fn_cfgmap}" EXIT

echo_log "Creating route"
fission route create --function ${fn_cfgmap} --url /${fn_cfgmap} --method GET

echo_log "Waiting for router to catch up"
sleep 5

echo_log "HTTP GET on the function's route"
rescfg=$(curl http://${FISSION_ROUTER}/${fn_cfgmap})

if [ ${rescfg} != ${val} ]
then
	echo_log "test cfgmap failed"
	cleanup
	exit 1
fi
echo_log "test configmap passed"

echo_log "testing creating a function without a secret or configmap"
fission function create --name ${fn} --env python --code empty.py
trap "fission fn delete --name ${fn}" EXIT

echo_log "Creating route"
fission route create --function ${fn} --url /${fn} --method GET

echo_log "Waiting for router to catch up"
sleep 5

echo_log "HTTP GET on the function's route"
resnormal=$(curl http://${FISSION_ROUTER}/${fn})
if [ ${resnormal} != "yes" ]
then
	echo_log "test empty failed"
	cleanup
	exit 1
fi
echo_log "test empty passed"

echo_log "All done."
trap "cleanup" EXIT
