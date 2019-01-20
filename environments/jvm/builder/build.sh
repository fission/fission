#!/bin/sh
set -eou pipefail
# Re - disableClassPathURLCheck flag: Latest Surefire plugin has bugs with JDK8 and causes VM to crash, check: https://stackoverflow.com/questions/23260057/the-forked-vm-terminated-without-saying-properly-goodbye-vm-crash-or-system-exi
mvn clean package -DargLine="-Djdk.net.URLClassPath.disableClassPathURLCheck=true"
cp ${SRC_PKG}/target/*with-dependencies.jar ${DEPLOY_PKG}