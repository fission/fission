#!/bin/sh

# TODO placeholder until we have better tests :)

set +x
set -e 

DIR=$(dirname $0)

echo "-- Starting server"
node $DIR/../server.js --codepath $DIR/test.js --port 8888 &
sleep 2

echo "-- Specializing"
curl -f -X POST http://localhost:8888/specialize 

echo "-- Running user function"
curl -f http://localhost:8888 ; echo

echo "-- Cleanup"
kill %1

