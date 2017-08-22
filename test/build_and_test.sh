#!/bin/bash

set -euo pipefail

if [ ! -f ${HOME}/.kube/config ]
then
    echo "Skipping end to end tests, no cluster credentials"
    exit 0
fi

source $(dirname $0)/test_utils.sh

IMAGE=gcr.io/fission-ci/fission-bundle
FETCHER_IMAGE=gcr.io/fission-ci/fetcher
TAG=test

build_and_push_fission_bundle $IMAGE:$TAG

build_and_push_fetcher $FETCHER_IMAGE:$TAG

build_fission_cli

install_and_test $IMAGE $TAG $FETCHER_IMAGE $TAG
