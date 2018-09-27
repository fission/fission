#!/bin/bash

DEMO_RUN_FAST=1
ROOT_DIR=$(dirname $0)/..
. $ROOT_DIR/util.sh

desc "Declaratively specified app"
run "ls specs"

desc "Generate initial YAML, so we don't have to write it by hand"
run "fission function create --spec --name hello-go --env go --src hello.go --entrypoint Handler"

desc "Generated function YAML"
run "tail -25 specs/function-hello-go.yaml"

desc "Deploy on cluster, and wait for build result"
run "fission spec apply --wait"

desc "Invoke the function"
run "fission function test --name hello-go"

desc "Live-reload: auto build + deploy on save"
run "fission spec apply --watch"

