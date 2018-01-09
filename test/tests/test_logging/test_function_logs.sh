#!/bin/bash

set -euo pipefail

ROOT=$(dirname $0)/../..

fn=nodejs-logtest-$(date +%N)

function cleanup {
    echo "Cleanup route"
    var=$(fission route list | grep $fn | awk '{print $1;}')
    fission route delete --name $var
}

# Create a hello world function in nodejs, test it with an http trigger
echo "Pre-test cleanup"
fission env delete --name nodejs || true

echo "Creating nodejs env"
fission env create --name nodejs --image fission/node-env
trap "fission env delete --name nodejs" EXIT

echo "Creating function"
fission fn create --name $fn --env nodejs --code log.js
trap "fission fn delete --name $fn" EXIT

echo "Creating route"
fission route create --function $fn --url /$fn --method GET
trap cleanup EXIT

echo "Waiting for router to catch up"
sleep 3

echo "Doing 4 HTTP GETs on the function's route"
for i in 1 2 3 4
do
    curl -s http://$FISSION_ROUTER/$fn
done

echo "Grabbing logs, should have 4 calls in logs"

sleep 15

logs=$(fission function logs --name $fn --detail)
echo "---function logs---"
echo $logs
echo "------"
num=(cat $logs | grep 'log test' | wc -l)
echo $num logs found

if [ $num -ne 4 ]
then
    echo "Test Failed: expected 4, found $num logs"
fi
 
echo "All done."
