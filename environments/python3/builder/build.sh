#!/bin/bash
set -e

builderDir=${GOPATH}/src/github.com/fission/fission/builder/cmd
pushd ${builderDir}
./build.sh
popd
cp ${builderDir}/builder .
