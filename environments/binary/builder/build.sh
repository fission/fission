#!/bin/sh

apk update

CWD=$(pwd)

if [ -f ${SRC_PKG}/setup.sh ]; then
    cd ${SRC_PKG}
    ./setup.sh
    cd ${CWD}
fi

cp -rf ${SRC_PKG} ${DEPLOY_PKG}
