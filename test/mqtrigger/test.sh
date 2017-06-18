#!/bin/bash

clusterID="fissionMQTrigger"
topic="foo.bar"
resptopic="foo.foo"
expectedRespOutput="[foo.foo]: 'Hello, World!'"
FISSIONDIR=$GOPATH"/src/github.com/fission/fission"

if [[ -z $NATS_STREAMING_URL ]]; then
    echo "'NATS_STREAMING_URL' must not be empty. For example: export NATS_STREAMING_URL=nats://192.168.0.1:4222"
    exit 1
fi

if [[ -z $FISSION_URL ]]; then
    echo "'FISSION_URL' must not be empty. For example: export FISSION_URL=http://10.10.10.10"
    exit 1
fi

cd $FISSIONDIR"/fission/"
go build
mv fission $FISSIONDIR"/test/mqtrigger"
cd $FISSIONDIR"/test/mqtrigger"

./fission env create --name nodejs --image fission/node-env
./fission fn create --name hello1 --env nodejs --code main.js --method GET
./fission route create --method GET --url /h1 --function hello1
./fission mqtrigger create --name h1 --function hello1 --mqtype "nats-streaming" --topic "foo.bar" --resptopic "foo.foo"

go run ./stan-pub.go -s $NATS_STREAMING_URL -c $clusterID -id clientPub $topic "" || exit 1

response=$(go run ./stan-sub.go --last -s $NATS_STREAMING_URL -c $clusterID -id clientSub $resptopic 2>&1)

if [[ "$response" != "$expectedRespOutput" ]]; then
    echo "$response is not equal to $expectedRespOutput"
    exit 1
fi

echo "Subscriber received expected response: $response"

exit 0