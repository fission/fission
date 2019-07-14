#!/bin/bash

set -euo pipefail

# Unbound variables cause failure, so this readable if block instead of Parameter Expansion
if [[ ${TRAVIS_EVENT_TYPE+NOVALUE} == "cronNOVALUE" ]]
then
    echo "Skipping build & test, this is cron job for fission upgrade tests"
    exit 0
fi

if [ ! -f ${HOME}/.kube/config ]
then
    echo "Skipping end to end tests, no cluster credentials"
    exit 0
fi

source $(dirname $0)/test_utils.sh

REPO=gcr.io/fission-ci
IMAGE=fission-bundle
FETCHER_IMAGE=$REPO/fetcher
BUILDER_IMAGE=$REPO/builder
TAG=${TRAVIS_BUILD_ID}
PRUNE_INTERVAL=1 # this variable controls the interval to run archivePruner. The unit is in minutes.
ROUTER_SERVICE_TYPE=LoadBalancer
SERVICE_TYPE=LoadBalancer
PRE_UPGRADE_CHECK_IMAGE=$REPO/pre-upgrade-checks

dump_system_info

while true; do
    # ensure that gke cluster is now free for testing
    resp=$(kubectl --namespace default create configmap in-test|grep "created"||true)
    if [[ -z $resp ]]; then
        echo "Cluster is now in used. Retrying after 15 seconds..."
        sleep 15
        continue
    fi
    kubectl --namespace default label configmap in-test travidID=${TRAVIS_BUILD_ID}
    break
done

install_and_test $REPO $IMAGE $TAG $FETCHER_IMAGE $TAG $PRUNE_INTERVAL $ROUTER_SERVICE_TYPE $SERVICE_TYPE $PRE_UPGRADE_CHECK_IMAGE
