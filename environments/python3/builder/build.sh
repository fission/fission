#!/bin/bash
set -e

builderDir=${GOPATH}/src/github.com/fission/fission/builder/cmd
pushd ${builderDir}
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o builder .
popd
cp ${builderDir}/builder .
