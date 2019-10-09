#!/bin/bash

set -eux

srcDir=${GOPATH}/src/$(basename ${SRC_PKG})

trap "rm -rf ${srcDir}" EXIT

# http://ask.xmodulo.com/compare-two-version-numbers.html
version_ge() { test "$(echo "$@" | tr " " "\n" | sort -rV | head -n 1)" == "$1"; }

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

if [ ! -z ${GOLANG_VERSION} ] && version_ge ${GOLANG_VERSION} "1.12"; then
    if [ -f "go.mod" ]; then
        go mod download
    else
        # still need to do this; otherwise, go will complain "cannot find main module".
        go mod init
    fi
else # go version lower than go 1.12
    if [ -f "go.mod" ]; then
        echo "Please update fission/go-builder and fission/go-env image to the latest version to support go module"
        exit 1
    fi
fi

# use vendor mode if the vendor dir exists when go version is greater
# than 1.12 (the version that fission started to support go module).
if  [ -d "vendor" ] && [ ! -z ${GOLANG_VERSION} ] && version_ge ${GOLANG_VERSION} "1.12"; then
  go build -mod=vendor -buildmode=plugin -i -o ${DEPLOY_PKG} .
else
  go build -buildmode=plugin -i -o ${DEPLOY_PKG} .
fi
