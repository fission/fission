#!/bin/sh

set -e

# check syntax
python3 -m compileall -l ${SRC_PKG}

# install deps
pip3 install -r ${SRC_PKG}/requirements.txt -t ${SRC_PKG} && cp -r ${SRC_PKG} ${DEPLOY_PKG}
