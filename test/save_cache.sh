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

dump_system_info

setupCIBuildEnv

mkdir -p ${DOCKER_CACHE_DIR}

docker save $(docker history -q $REPO/python-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/python-env.tar.gz || true
docker save $(docker history -q $REPO/jvm-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/jvm-env.tar.gz || true
docker save $(docker history -q $REPO/go-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/go-env.tar.gz || true
docker save $(docker history -q $REPO/tensorflow-serving-env:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/tensorflow-serving-env.tar.gz || true

docker save $(docker history -q $REPO/python-env-builder:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/python-builder.tar.gz || true
docker save $(docker history -q $REPO/jvm-env-builder:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/jvm-builder.tar.gz || true
docker save $(docker history -q $REPO/go-env-builder:$TAG | grep -v '<missing>') | gzip > ${DOCKER_CACHE_DIR}/go-builder.tar.gz || true
