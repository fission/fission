#!/bin/bash

set -e

GOOS=linux GOARCH=386 go build

docker build -t fission-bundle .
docker tag fission-bundle fission/fission-bundle
docker push fission/fission-bundle
