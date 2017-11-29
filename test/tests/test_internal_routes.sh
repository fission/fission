#!/bin/bash

#
# Create two functions, make sure their internal http triggers invoke
# them correctly.
#

set -euo pipefail

ROOT=$(dirname $0)/../..

echo "Pre-test cleanup"
fission env delete --name nodejs || true

echo "Creating nodejs env"
fission env create --name nodejs --image fission/node-env
trap "fission env delete --name nodejs" EXIT

echo "Writing functions"
f1=f1-$(date +%s)
f2=f2-$(date +%s)
echo $f1 $f2

for f in $f1 $f2
do
    echo "module.exports = function(context, callback) { callback(200, \"$f\n\"); }" > $f.js
done

echo "Creating functions"
for f in $f1 $f2
do
    fission fn create --name $f --env nodejs --code $f.js
    trap "fission fn delete --name $f" EXIT
done

echo "Waiting for router to catch up"
sleep 4

echo "Testing internal routes"
for f in $f1 $f2
do
    response=$(curl http://$FISSION_ROUTER/fission-function/$f)
    echo $response | grep $f
done

echo "All done."
