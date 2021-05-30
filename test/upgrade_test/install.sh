#!/bin/bash

RANDOM=124

generate_test_id() {
    echo $(((10000 + $RANDOM) % 99999))
}

ROOT=$(pwd)
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


sleep 10

echo "Running new fission build..."

docker build -t fission-bundle -f cmd/fission-bundle/Dockerfile.fission-bundle .
docker build -t fetcher -f cmd/fetcher/Dockerfile.fission-fetcher .
docker build -t builder -f cmd/builder/Dockerfile.fission-builder .
docker build -t reporter -f cmd/reporter/Dockerfile.reporter .

sleep 5

echo "fission helm upgrade....."

REPO=""
IMAGE=fission-bundle
FETCHER_IMAGE=fetcher
BUILDER_IMAGE=builder
TAG=latest
PRUNE_INTERVAL=1 # Unit - Minutes; Controls the interval to run archivePruner.
ROUTER_SERVICE_TYPE=ClusterIP

helmVars=repository=$REPO,image=$IMAGE,imageTag=$TAG,fetcher.image=$FETCHER_IMAGE,fetcher.imageTag=$TAG,analytics=false,pruneInterval=60,routerServiceType=LoadBalancer,prometheus.enabled=false

echo "Upgrading fission"
helm upgrade	\
 --timeout 540s	 \
 --set $helmVars \
 --namespace $ns  \
 fission \
 $ROOT/charts/fission-all

sleep 30

kubectl get pods -A