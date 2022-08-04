#!/bin/bash
#test:disabled

# Create a function and trigger it using Kafka
# This test requires KEDA and Kafka in the cluster
# This test assumes that there are three topics are already created with 3 Partitions and 3 Replicas in the cluster

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
mqt="kafkatest-$TEST_ID"
no_of_topic_partition=3 # Change this if number of partitions are different
topic="topic2"
resptopic="response-topic"
errortopic="error-topic"
bootstrap_server="my-cluster-kafka-brokers.my-kafka-project.svc:9092"
consumer_group="my-group"
cooldownperiod=30
pollinginterval=30

export FISSION_ROUTER=127.0.0.1:11009

test_totalpods(){
    echo "Checking total number of scaledpods"

    set +e
    while true; do
        
        cnt=$(kubectl get deployment ${mqt} -o jsonpath='{.status.readyReplicas}')
        if [$cnt -ge $no_of_topic_partition]; then
            break
        fi
        sleep 1
    done
    set -e
}

export -f test_totalpods

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
pkgName=$(fission package create --name kafka-${TEST_ID} --env ${goenv} --src kafka.zip|cut -f2 -d' '| tr -d \')

log "pkgName=${pkgName}"
popd

timeout 300s bash -c "waitBuild $pkgName"
log "Package ${pkgName} created"
log "Creating function ${producerfunc}"
fission fn create --name ${producerfunc} --env ${goenv} --pkg ${pkgName} --entrypoint Handler
log "Creating function ${consumerfunc}"
fission fn create --name ${consumerfunc} --env ${nodeenv} --code $DIR/hellokafka.js 

log "Creating trigger $mqt"
fission mqt create --name ${mqt} --function ${consumerfunc} --mqtype kafka --topic $topic --resptopic $resptopic --errortopic $errortopic --version2=true --metadata bootstrapServers=$bootstrap_server --metadata consumerGroup=$consumer_group --metadata topic=$topic --cooldownperiod=$cooldownperiod --pollinginterval=$pollinginterval

log "Create Route to producer function"
fission route create --function ${producerfunc} --url /${producerfunc} --method GET

log "Run Producer function"
funcURL=$FISSION_ROUTER/${producerfunc}
success="Successfully sent to testtopic"
echo $funcURL

timeout 100s bash -c "test_response $funcURL $success"

timeout 100s bash -c "test_totalpods"

log "Test PASSED"
