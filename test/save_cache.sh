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

mkdir -p ${DOCKER_CACHE_DIR}

docker save $(docker history -q $REPO/python-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/python-env.tar.gz;
docker save $(docker history -q $REPO/jvm-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/jvm-env.tar.gz;
docker save $(docker history -q $REPO/go-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/go-env.tar.gz;
docker save $(docker history -q $REPO/tensorflow-serving-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/tensorflow-serving-env.tar.gz;

docker save $(docker history -q $REPO/python-env-builder:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/python-builder.tar.gz;
docker save $(docker history -q $REPO/jvm-env-builder:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/jvm-builder.tar.gz;
docker save $(docker history -q $REPO/go-env-builder:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/go-builder.tar.gz;
