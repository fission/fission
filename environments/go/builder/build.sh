#!/bin/sh

set -eux

srcDir=${GOPATH}/src/$(basename ${SRC_PKG})

trap "rm -rf ${srcDir}" EXIT

if [ -d ${SRC_PKG} ]
then
    echo "Building in directory ${srcDir}"
    ln -sf ${SRC_PKG} ${srcDir}
elif [ -f ${SRC_PKG} ]
then
    echo "Building file ${SRC_PKG} in ${srcDir}"
    mkdir -p ${srcDir}
    cp ${SRC_PKG} ${srcDir}/function.go
fi

cd ${srcDir}
go build -buildmode=plugin -i -o ${DEPLOY_PKG} .
