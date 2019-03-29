#!/bin/bash
#test:disabled

# Create a function and trigger it using Kafka
# This test requires Kafka & MQ-Kafka component of Fission installed in the cluster
set -euo pipefail
set +x

nodeenv="node-kafka"
goenv="go-kafka"
producerfunc="producer-func"
consumerfunc="consumer-func"
consumerfunc2="consumer-func2"

log() {
    echo $1
}
export -f log

test_mqmessage() {
    echo "Checking for valid response"

    while true; do
      response0=$(kubectl -nfission logs -l=messagequeue=kafka)
      echo $response0 | grep -i $1
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
}
export -f test_mqmessage 

test_fnmessage() {
    # $1: functionName
    # $2: container name
    # $3: string to look for
    echo "Checking for valid function log"

    while true; do
	response0=$(kubectl -nfission-function logs -l=functionName=$1 -c $2)
	echo $response0 | grep -i "$3"
	if [[ $? -eq 0 ]]; then
	    break
	fi
	sleep 1
    done
}
export -f test_fnmessage

waitBuild() {
    log "Waiting for builder manager to finish the build"
    
    while true; do
      kubectl --namespace default get packages $1 -o jsonpath='{.status.buildstatus}'|grep succeeded
      if [[ $? -eq 0 ]]; then
          break
      fi
      log "Waiting for build to finish"
      sleep 1
    done
}
export -f waitBuild

cleanup() {
    log "Cleaning up..."
    fission env delete --name ${goenv} || true
    fission env delete --name ${nodeenv} || true
    fission fn delete --name ${producerfunc} || true
    fission fn delete --name ${consumerfunc} || true
    fission fn delete --name ${consumerfunc2} || true
    fission mqt delete --name kafkatest || true
    fission mqt delete --name kafkatest2 || true
}
export -f cleanup

DIR=$(dirname $0)

log "Creating ${nodeenv} environment"
fission env create --name ${nodeenv} --image fission/node-env
trap cleanup EXIT

log "Creating ${goenv} environment"
fission env create --name ${goenv} --image fission/go-env --builder fission/go-builder

log "Creating package for Kafka producer"
pushd $DIR/kafka_pub
go mod vendor
zip -qr kafka.zip * 
pkgName=$(fission package create --env ${goenv} --src kafka.zip|cut -f2 -d' '| tr -d \')

log "pkgName=${pkgName}"
popd

timeout 120s bash -c "waitBuild $pkgName"
log "Package ${pkgName} created"

log "Creating function ${consumerfunc}"
fission fn create --name ${consumerfunc} --env ${nodeenv} --code hellokafka.js 

log "Creating function ${consumerfunc2}"
fission fn create --name ${consumerfunc2} --env ${nodeenv} --code hellokafka.js

log "Creating function ${producerfunc}"
fission fn create --name ${producerfunc} --env ${goenv} --pkg ${pkgName} --entrypoint Handler

log "Creating trigger kafkatest"
fission mqt create --name kafkatest --function ${consumerfunc} --mqtype kafka --topic testtopic --resptopic resptopic

log "Creating trigger kafkatest2"
fission mqt create --name kafkatest2 --function ${consumerfunc2} --mqtype kafka --topic resptopic

fission fn test --name ${producerfunc}

log "Testing pool manager function"
timeout 60 bash -c "test_mqmessage 'testvalue'"

log "Testing the headers values in ${consumerfunc}"
timeout 60 bash -c "test_fnmessage '${consumerfunc}' '${nodeenv}' 'z-custom-name: Kafka-Header-test'"

log "Testing the header value in ${consumerfunc2}"
timeout 60 bash -c "test_fnmessage '${consumerfunc2}' '${nodeenv}' 'z-custom-name: Kafka-Header-test'"
# test if the Fission specific headers are overwritten
timeout 60 bash -c "test_fnmessage '${consumerfunc2}' '${nodeenv}' 'x-fission-function-name: consumer-func2'"
