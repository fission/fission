#!/bin/bash

set -e

tag=$1
if [ -z "$tag" ]
then
    tag=latest
fi

. build.sh

docker build -t fission-bundle .
docker tag fission-bundle fission/fission-bundle:$tag
docker push fission/fission-bundle:$tag
