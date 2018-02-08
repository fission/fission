#!/bin/sh

if [ -d ${SRC_PKG} ]
then
    echo "Building in directory ${SRC_PKG}"
    cd ${SRC_PKG}
    go build -buildmode=plugin -i -o ${DEPLOY_PKG} .
elif [ -f ${SRC_PKG} ]
then
    fn=$(basename ${SRC_PKG})
    d=/tmp/$fn
    mkdir $d
    trap "rm -rf /tmp/$fn" EXIT
    echo "Building file ${SRC_PKG} in $d"

    cd $d
    cp ${SRC_PKG} ./function.go
    go build -buildmode=plugin -i -o ${DEPLOY_PKG} .
fi
