#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../../..

cleanup() {
    fission fn delete --name hellon
    fission fn delete --name hellop
    fission env delete --name jvm
}

cd $ROOT/examples/jvm/java

log "Creating the jar from application"
#Using Docker to build Jar so that maven & other Java dependencies are not needed on CI server
docker run -it --rm  -v "$(pwd)":/usr/src/mymaven -w /usr/src/mymaven maven:3.5-jdk-8 mvn clean package

log "Creating environment for Java"
fission env create --name jvm --image fission/jvm-env --version 2 --extract=false

log "Creating pool manager & new deployment function for Java"
fission fn create --name hellop --deploy target/hello-world-1.0-SNAPSHOT-jar-with-dependencies.jar --env jvm --entrypoint io.fission.HelloWorld
fission fn create --name hellon --deploy target/hello-world-1.0-SNAPSHOT-jar-with-dependencies.jar --env jvm --executortype newdeploy --entrypoint io.fission.HelloWorld
trap cleanup EXIT

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function"
response=$(curl http://$FISSION_ROUTER/fission-function/hellop)

log "Checking for valid response"
echo $response
echo $response | grep -i hello

log "Testing new deployment function"
echo $response
response=$(curl http://$FISSION_ROUTER//fission-function/hellon)

log "Checking for valid response"
echo $response | grep -i hello