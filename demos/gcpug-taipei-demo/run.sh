#!/bin/bash

DEMO_RUN_FAST=1
ROOT_DIR=$(dirname $0)/..
. $ROOT_DIR/util.sh

export FISSION_ROUTER="35.221.188.239"

#
# General setup intro
#
clear

desc "Our Kubernetes cluster"
run "kubectl get nodes"

desc "Fission is installed"
run "kubectl --namespace fission get deployment"

#
# Hello world, environments, cold starts
#
clear

desc "How to create a serverless function"
run

desc "Hello world function"
run "cat hello.js"

desc "Add the NodeJS environment to fission"
run "fission env create --name nodejs --image fission/node-env"

desc "NodeJS environment pods"
run "kubectl --namespace fission-function get pod -l environmentName=nodejs"

desc "Upload our function to fission"
run "fission function create --name hello --env nodejs --code hello.js"

desc "Set up a route (aka HTTP Trigger) for the function"
run "fission route create --method GET --url /hello --function hello"

sleep 2
clear

desc "Finally, run the function"
run "time -p curl http://$FISSION_ROUTER/hello"

desc "Run the function again"
run "time -p curl http://$FISSION_ROUTER/hello"
run "time -p curl http://$FISSION_ROUTER/hello"

desc "The pod is now labeled by the function name"
run "kubectl --namespace fission-function get pod -l functionName=hello"

#
# Declarative Specs.
#
# Use go for this one to show off the fact that we can do builds.
#
clear

desc "How to create a serverless function"
run

rm -rf fission-bank-sample || true

desc "Clone application from version control system (Github)"
run "git clone https://github.com/fission/fission-bank-sample.git"

pushd fission-bank-sample > /dev/null

desc "Declaratively specified app"
run "ls"

desc "Declarative specification files"
run "ls specs"

clear

desc "Create database service in different namespace"
run "kubectl apply -f cockroachdb.yaml"

desc "Download dependencies (Needed for Go env for now)\n(For Python & Node, builder manger will download dependencies automatically)"
pushd functions/ > /dev/null
run "glide install -v"
popd > /dev/null

clear

desc "Validate configs"
run "fission spec validate"

desc "Deploy application, wait for build to finish"
run "fission spec apply --wait"

clear

desc "Check package build information details"
run "fission pkg info --name bank-go-pkg"

desc "Invoke our new function"
run "echo http://$FISSION_ROUTER/bank/login.html"

desc "Remove application"
run "fission spec destroy"
run "kubectl delete -f cockroachdb.yaml"

popd > /dev/null

#
# Canary Deployments
#
clear

desc "Perform canary deployment/rollback"
run

# set up functions
fission fn create --name func-v1 --env nodejs --code func-v1.js
desc "Function version 1"
run "fission function get --name func-v1"

fission fn create --name func-v2 --env nodejs --code func-v2.js
desc "Function version 2"
run "fission function get --name func-v2"

clear

desc "Create a route (HTTP trigger) the version-1 of the function with weight 100% and version-2 with weight 0%"
run "fission route create --name route-canary --method GET --url /canary --function func-v1 --weight 100 --function func-v2 --weight 0"

desc "Start sending requests to the route"
run_bg "hey -n 100000 -c 1 http://$FISSION_ROUTER/canary"

desc "Create a canary config: with an increment of 10 percent, every 1 minute, rolling back if 10% of requests fail"
run "fission canary-config create --name canary-1 --funcN func-v2 --funcN-1 func-v1 --httptrigger route-canary --increment-step 10 --increment-interval 30s --failure-threshold 10"
