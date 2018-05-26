#!/bin/sh

if [ -d ${SRC_PKG} ]
then
    echo "Building in directory ${SRC_PKG}"
    cd ${SRC_PKG}

    if [ -f glide.yaml ]; then
        go get -u github.com/Masterminds/glide
        glide install
    fi

    if [ -d vendor ]; then
        cp -r vendor/* /usr/src/
    fi

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
