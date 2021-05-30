#!/bin/bash

RANDOM=124

generate_test_id() {
    echo $(((10000 + $RANDOM) % 99999))
}

ROOT=/root/clone/fission/
#pushd $ROOT_RELPATH
#ROOT=$(pwd)
#popd

# This will change for every new release
CURRENT_VERSION=1.12.0

#source /root/clone/fission/test/test_utils.sh
#source /root/clone/fission//test/upgrade/fixture_tests.sh

id=$(generate_test_id)
ns=f-$id
fns=f-func-$id
controllerNodeport=31234
pruneInterval=1
routerServiceType=LoadBalancer

helmVars=functionNamespace=$fns,controllerPort=$controllerNodeport,pullPolicy=Always,analytics=false,pruneInterval=$pruneInterval,routerServiceType=$routerServiceType

#dump_system_info


echo "Deleting old releases"
helm list -q|xargs -I@ bash -c "helm_uninstall_fission @"

# deleting ns does take a while after command is issued
while kubectl get ns| grep "fission-builder"
do
    sleep 5
done

echo "Creating namespace $ns"
kubectl create ns $ns
helm install \
--namespace $ns \
--name-template fission \
https://github.com/fission/fission/releases/download/${CURRENT_VERSION}/fission-all-${CURRENT_VERSION}.tgz

mkdir temp && cd temp && curl -Lo fission https://github.com/fission/fission/releases/download/${CURRENT_VERSION}/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/ && cd .. && rm -rf temp
sleep 60
kubectl get pods -A
fission env create --name nodejs --image fission/node-env:latest
sleep 5
curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
fission function create --name hello --env nodejs --code hello.js
sleep 5
fission function test --name hello