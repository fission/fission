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
read -p "Publish fission/fission-bundle:${tag}? " -n 1 -r
echo    # (optional) move to a new line
if [[ $REPLY =~ ^[Yy]$ ]]
then
    docker push fission/fission-bundle:$tag
fi
