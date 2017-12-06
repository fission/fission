#!/bin/sh

go build -buildmode=plugin -o ${DEPLOY_PKG} ${SRC_PKG}
