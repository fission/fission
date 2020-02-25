#!/bin/bash

test:disabled

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

ROOT=$(dirname $0)/../../..

env=jvm-$TEST_ID
fn_n=jvm-hello-n-$TEST_ID
fn_p=jvm-hello-p-$TEST_ID

cleanup() {
    clean_resource_by_id $TEST_ID
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

cd $ROOT/examples/jvm/java

log "Creating the jar from application"
#Using Docker to build Jar so that maven & other Java dependencies are not needed on CI server
docker run -it --rm  -v "$(pwd)":/usr/src/mymaven -w /usr/src/mymaven maven:3.5-jdk-8 mvn clean package -q

log "Creating environment for Java"
fission env create --name $env --image $JVM_RUNTIME_IMAGE --version 2 --keeparchive=true

log "Creating pool manager & new deployment function for Java"
fission fn create --name $fn_p --deploy target/hello-world-1.0-SNAPSHOT-jar-with-dependencies.jar --env $env --entrypoint io.fission.GetFunctionData
fission fn create --name $fn_n --deploy target/hello-world-1.0-SNAPSHOT-jar-with-dependencies.jar --env $env --executortype newdeploy --entrypoint io.fission.GetFunctionData

log "Creating route for pool manager function"
fission route create --name $fn_p --function $fn_p --url /$fn_p --method POST

log "Creating route for new deployment function"
fission route create --name $fn_n --function $fn_n --url /$fn_n --method POST

log "Waiting for router & pools to catch up"
sleep 5

body='<XML>
        <Book>
	        <name>Ashish</name>
            <subject>
                <name>Math</name>
                <name>Hindi</name>
            </subject>
        </Book>
    </XML>'

expect='<XML>
        <Book>
	        <name>Ashish</name>
            <subject>
                <name>Math</name>
                <name>Hindi</name>
            </subject>
        </Book>
    </XML>'
header='Content-type: application/xml'
#test if the function is returning the same data as we passed
log "Testing pool manager function"
timeout 60 bash -c "test_xml_fn $fn_p \"$body\" \"$expect\" \"$header\""

log "Testing new deployment function"
timeout 60 bash -c "test_xml_fn $fn_n \"$body\" \"$expect\" \"$header\""

log "Test PASSED"