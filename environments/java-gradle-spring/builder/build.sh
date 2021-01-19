#!/bin/sh

set -eou pipefail

gradle build

# TODO: build to specific filename
cp ${SRC_PKG}/build/libs/*-0.0.1-SNAPSHOT.jar ${DEPLOY_PKG}
