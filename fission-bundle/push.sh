#!/bin/bash

set -e

. build.sh

docker build -t fission-bundle .
docker tag fission-bundle fission/fission-bundle
docker push fission/fission-bundle
