#!/bin/bash

DEMO_RUN_FAST=1
ROOT_DIR=$(dirname $0)/..
. $ROOT_DIR/util.sh

desc "Kubernetes cluster"
run "kubectl get nodes"

desc "Fission installed"
run "kubectl --namespace fission get deployment"

clear

desc "Hello world function"
run "cat hello.js"

desc "Add NodeJS environment to fission"
run "fission env create --name nodejs --image fission/node-env"

desc "NodeJS environment pods"
run "kubectl --namespace fission-function get pod -l environmentName=nodejs"

desc "Upload a function to fission"
run "fission function create --name hello --env nodejs --code hello.js"

desc "Set up a route \(HTTP trigger\) for the function"
run "fission route create --method GET --url /hello --function hello"

sleep 2

desc "Finally, run the function"
run "time -p curl http://$FISSION_ROUTER/hello"

desc "Run the function again"
run "time -p curl http://$FISSION_ROUTER/hello"
run "time -p curl http://$FISSION_ROUTER/hello"

desc "The pod is now labeled by the function name"
run "kubectl --namespace fission-function get pod -l functionName=hello"
