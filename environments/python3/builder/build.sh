#!/bin/bash
set -e

tag=$1
if [ -z "$tag" ]
then
    tag=latest
fi

builderDir=${GOPATH}/src/github.com/fission/fission/builder/cmd
pushd ${builderDir}
GOOS=linux GOARCH=amd64 go build -o builder .
popd
cp ${builderDir}/builder .

