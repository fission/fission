#!/bin/bash
set -euxo pipefail 
cd ${SRC_PKG}
#now start execution of custom logic dll in such a way that it should copy everything in ${SRC_PKG}
#first lets try putting sample file which will prove that this worked
echo src : ${SRC_PKG} ,dest:  ${DEPLOY_PKG} >builderpaths.txt

#now run actual dll for custom builder logic
# please note as this need to be executed from app folder so that all dependent files are available
# else you will end up getting File not found error
cd /app

#now execute dll
dotnet Builder.dll

#copy entire content to deployment package
cp -r ${SRC_PKG} ${DEPLOY_PKG}
