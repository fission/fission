#!/bin/bash

set -euo pipefail

source test_utils.sh

IMAGE=gcr.io/fission-ci/fission-bundle
TAG=test

build_and_push_fission_bundle $IMAGE:$TAG

build_fission_cli

install_and_test $IMAGE $TAG
