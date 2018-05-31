#!/bin/sh

# My go build script


cd ${SRC_PKG}

go get github.com/zbiljic/rands

go build -buildmode=plugin -i -o ${DEPLOY_PKG} .
