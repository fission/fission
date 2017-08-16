#!/bin/bash

DEMO_RUN_FAST=1
ROOT_DIR=$(dirname $0)/..
. $ROOT_DIR/util.sh

desc "Deploy redis"
run "kubectl create -f redis.yaml"

desc "Ensure fission has a python environment"
run "fission env create --name python --image fission/python-env"

desc "Register fission functions"
run "fission function create --name guestbook-get --env python --code get.py --url /guestbook --method GET"
run "fission function create --name guestbook-add --env python --code add.py --url /guestbook --method POST"

echo "http://$FISSION_ROUTER/guestbook"

