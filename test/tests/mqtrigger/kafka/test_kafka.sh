#!/bin/bash
#test:disabled

# Create a function and trigger it using Kafka
# This test requires Kafka & MQ-Kafka component of Fission installed in the cluster
set -euo pipefail
source $(dirname $0)/../../../utils.sh
set +x

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

nodeenv="node-kafka-$TEST_ID"
goenv="go-kafka-$TEST_ID"
producerfunc="producer-func-$TEST_ID"
consumerfunc="consumer-func-$TEST_ID"
consumerfunc2="consumer-func2-$TEST_ID"
mqt="kafkatest-$TEST_ID"
mqt2="kafkatest2-$TEST_ID"
topic="testtopic-$TEST_ID"
resptopic="resptopic-$TEST_ID"

test_mqmessage() {
    echo "Checking for valid response"

    set +e
    while true; do
      response0=$(kubectl -nfission logs -l=messagequeue=kafka)
      echo $response0 | grep -i $1
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
    set -e
}
export -f test_mqmessage 

test_fnmessage() {
    # $1: functionName
    # $2: container name
    # $3: string to look for
    echo "Checking for valid function log"

    set +e
    while true; do
	response0=$(kubectl -nfission-function logs -l=functionName=$1 -c $2)
	echo $response0 | grep -i "$3"
	if [[ $? -eq 0 ]]; then
	    break
	fi
	sleep 1
    done
    set -e
}
export -f test_fnmessage

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

DIR=$(dirname $0)

log "Creating ${nodeenv} environment"
fission env create --name ${nodeenv} --image ${NODE_RUNTIME_IMAGE}

log "Creating ${goenv} environment"
fission env create --name ${goenv} --image ${GO_RUNTIME_IMAGE} --builder ${GO_BUILDER_IMAGE}

log "Creating package for Kafka producer"
cp -r $DIR/kafka_pub $tmp_dir/
pushd $tmp_dir/kafka_pub
go mod vendor
zip -qr kafka.zip * 
pkgName=$(fission package create --env ${goenv} --src kafka.zip|cut -f2 -d' '| tr -d \')

log "pkgName=${pkgName}"
popd

timeout 120s bash -c "waitBuild $pkgName"
log "Package ${pkgName} created"

log "Creating function ${consumerfunc}"
fission fn create --name ${consumerfunc} --env ${nodeenv} --code $DIR/hellokafka.js 

log "Creating function ${consumerfunc2}"
fission fn create --name ${consumerfunc2} --env ${nodeenv} --code $DIR/hellokafka.js

log "Creating function ${producerfunc}"
fission fn create --name ${producerfunc} --env ${goenv} --pkg ${pkgName} --entrypoint Handler

log "Creating trigger $mqt"
fission mqt create --name ${mqt} --function ${consumerfunc} --mqtype kafka --topic $topic --resptopic $resptopic

log "Creating trigger $mqt2"
fission mqt create --name ${mqt2} --function ${consumerfunc2} --mqtype kafka --topic $resptopic

fission fn test --name ${producerfunc}

log "Testing pool manager function"
timeout 60 bash -c "test_mqmessage 'testvalue'"

log "Testing the headers values in ${consumerfunc}"
timeout 60 bash -c "test_fnmessage '${consumerfunc}' '${nodeenv}' 'z-custom-name: Kafka-Header-test'"

log "Testing the header value in ${consumerfunc2}"
timeout 60 bash -c "test_fnmessage '${consumerfunc2}' '${nodeenv}' 'z-custom-name: Kafka-Header-test'"
# test if the Fission specific headers are overwritten
timeout 60 bash -c "test_fnmessage '${consumerfunc2}' '${nodeenv}' 'x-fission-function-name: consumer-func2'"

log "Test PASSED"
