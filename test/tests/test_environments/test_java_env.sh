#!/bin/bash

#test:disabled

set -euo pipefail
source $(dirname $0)/../../utils.sh

ROOT=$(dirname $0)/../../..

cleanup() {
    fission fn delete --name hellon || true
    fission fn delete --name hellop || true
    fission env delete --name jvm || true
}

test_fn() {
    echo "Checking for valid response"

    while true; do
      response0=$(curl http://$FISSION_ROUTER/$1)
      echo $response0 | grep -i $2
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
}

export -f test_fn

cd $ROOT/examples/jvm/java

log "Creating the jar from application"
#Using Docker to build Jar so that maven & other Java dependencies are not needed on CI server
docker run -it --rm  -v "$(pwd)":/usr/src/mymaven -w /usr/src/mymaven maven:3.5-jdk-8 mvn clean package -q

log "Creating environment for Java"
fission env create --name jvm --image gcr.io/fission-ci/jvm-env:test --version 2 --keeparchive=true

log "Creating pool manager & new deployment function for Java"
fission fn create --name hellop --deploy target/hello-world-1.0-SNAPSHOT-jar-with-dependencies.jar --env jvm --entrypoint io.fission.HelloWorld
fission fn create --name hellon --deploy target/hello-world-1.0-SNAPSHOT-jar-with-dependencies.jar --env jvm --executortype newdeploy --entrypoint io.fission.HelloWorld
trap cleanup EXIT

log "Creating route for pool manager function"
fission route create --function hellop --url /hellop --method GET

log "Creating route for new deployment function"
fission route create --function hellon --url /hellon --method GET

log "Waiting for router & pools to catch up"
sleep 5

log "Testing pool manager function"
timeout 60 bash -c "test_fn hellop 'Hello'"

log "Testing new deployment function"
timeout 60 bash -c "test_fn hellon 'Hello'"
