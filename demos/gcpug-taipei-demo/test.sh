#!/bin/bash

DEMO_RUN_FAST=1
ROOT_DIR=$(dirname $0)/..
. $ROOT_DIR/util.sh


desc "one"
run "sleep 30"

desc "two"
run "echo hi"
