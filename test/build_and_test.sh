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

log "~#~ start"

REPO=gcr.io/fission-ci
IMAGE=$REPO/fission-bundle
FETCHER_IMAGE=$REPO/fetcher
FLUENTD_IMAGE=gcr.io/fission-ci/fluentd
BUILDER_IMAGE=$REPO/builder
TAG=test
PRUNE_INTERVAL=1 # this variable controls the interval to run archivePruner. The unit is in minutes.
ROUTER_SERVICE_TYPE=LoadBalancer
SERVICE_TYPE=LoadBalancer
PRE_UPGRADE_CHECK_IMAGE=$REPO/pre-upgrade-checks

dump_system_info

log "~#~ building bundle"

build_and_push_fission_bundle $IMAGE:$TAG
log "~#~ finished build_and_push_fission_bundle"

build_and_push_pre_upgrade_check_image $PRE_UPGRADE_CHECK_IMAGE:$TAG
log "~#~ finished building preupgrade check img"

build_and_push_fetcher $FETCHER_IMAGE:$TAG

log "~#~ finished build_and_push_fetcher"

build_and_push_builder $BUILDER_IMAGE:$TAG

log "~#~ finished build_and_push_builder"

build_and_push_env_runtime python $REPO/python-env:$TAG
log "~#~ finished build_and_push_env_runtime python"

build_and_push_env_runtime jvm $REPO/jvm-env:$TAG
log "~#~ finished build_and_push_env_runtime jvm"

build_and_push_env_runtime go $REPO/go-env:$TAG
log "~#~ finished build_and_push_env_runtime go"

build_and_push_env_builder python $REPO/python-env-builder:$TAG $BUILDER_IMAGE:$TAG
log "~#~ finished build_and_push_env_builder python"
build_and_push_env_builder jvm $REPO/jvm-env-builder:$TAG $BUILDER_IMAGE:$TAG
log "~#~ finished build_and_push_env_builder jvm"
build_and_push_env_builder go $REPO/go-env-builder:$TAG $BUILDER_IMAGE:$TAG
log "~#~ finished build_and_push_env_builder go"

build_and_push_fluentd $FLUENTD_IMAGE:$TAG
log "~#~ finished build_and_push_fluentd"

build_fission_cli
log "~#~ finished build_fission_cli"

install_and_test $IMAGE $TAG $FETCHER_IMAGE $TAG $FLUENTD_IMAGE $TAG $PRUNE_INTERVAL $ROUTER_SERVICE_TYPE $SERVICE_TYPE $PRE_UPGRADE_CHECK_IMAGE
