#!/bin/sh
set -e

tag=$1
if [ -z "$tag" ]
then
    tag=latest
fi

builderDir=${GOPATH}/src/github.com/fission/fission/builder/cmd
pushd ${builderDir}
GOOS=linux GOARCH=386 go build -o builder .
popd
cp ${builderDir}/builder .
docker build -t python-builder .
docker tag python-builder fission/python-builder:$tag
docker push fission/python-builder:$tag

