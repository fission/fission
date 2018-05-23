#!/bin/bash

#
# Create a function and trigger it using NATS
#

set -euo pipefail
set +x

ROOT=$(dirname $0)/../..
DIR=$(dirname $0)

clusterID="fissionMQTrigger"
topic="foo.bar"
resptopic="foo.foo"
errortopic="foo.error"
maxretries=1
FISSION_NATS_STREAMING_URL="http://defaultFissionAuthToken@192.168.64.3:4222"
expectedRespOutput="[foo.error]: 'Hello, World!'"

echo "Pre-test cleanup"
fission env delete --name nodejs || true

echo "Creating nodejs env"
fission env create --name nodejs --image fission/node-env
trap "fission env delete --name nodejs" EXIT

echo "Creating function"
fn=hello-$(date +%s)
fission fn create --name $fn --env nodejs --code $DIR/main.js --method GET
trap "fission fn delete --name $fn" EXIT

echo "Creating message queue trigger"
mqt=mqt-$(date +%s)
fission mqtrigger create --name $mqt --function $fn --mqtype "nats-streaming" --topic $topic --resptopic $resptopic --errortopic $errortopic --maxretries $maxretries
echo "Updated mqtrigger list"
fission mqtrigger list
trap "fission mqtrigger delete --name $mqt" EXIT

# wait until nats trigger is created
sleep 5

#
# Send a message
#
echo "Sending message"
go run $DIR/stan-pub.go -s $FISSION_NATS_STREAMING_URL -c $clusterID -id clientPub $topic "Giraffes"

#
# Wait for message on error topic
#
echo "Waiting for response"
TIMEOUT=timeout
if [ $(uname -s) == 'Darwin' ]
then
    # If this fails on mac os, do "brew install coreutils".
    TIMEOUT=gtimeout
fi
response=$($TIMEOUT 240s go run $DIR/stan-sub.go --last -s $FISSION_NATS_STREAMING_URL -c $clusterID -id clientSub $errortopic 2>&1)

echo "Subscriber received expected response: $response"

if [[ "$response" != "$expectedRespOutput" ]]; then
    echo "$response is not equal to $expectedRespOutput"
    exit 1
fi
