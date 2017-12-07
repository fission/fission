#!/bin/sh

cd ${SRC_PKG}
go build -buildmode=plugin -i -o ${DEPLOY_PKG} .
