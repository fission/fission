#!/usr/bin/env bash

#### Purpose ####
# The purpose of this script is to enable running some/all of test scripts locally on mac so that 
# you don't have to always rely on CI cycle for feedback.

#### Usage ####
# For running a specific test:
# ./run_test_mac.sh test_spec.sh
#
# For running all tests
# ./run_test_mac.sh

#### Prerequisite ####
# Need to install following on Mac
# brew install coreutils  --> For (g)date & (g)timeout equivalents
# brew install gnu-sed --with-default-names  --> Sed's -i flag does not work without argument on Mac. Check: https://stackoverflow.com/questions/5694228/sed-in-place-flag-that-works-both-on-mac-bsd-and-linux/22084103#22084103
# 

#### Caution #### 
# Some scripts might use additional variables only available during CI cycle such as an image 
# being built by CI - which you will have to override manually in that script.
# Some tests known to fail as of now:
# test_mqtrigger_error.sh, test_mqtrigger.sh (Needs connection to MQ), test_archive_pruner.sh (The package somehow gets created in default package in local setup), test_obj_create_in_diff_ns.sh


set -euo pipefail

source $(dirname $0)/test_utils.sh

## Common env parameters
export FISSION_NAMESPACE=fission
export FUNCTION_NAMESPACE=fission-function
export FISSION_ROUTER=$(kubectl -n $FISSION_NAMESPACE get svc router -o jsonpath='{...ip}')
export FISSION_NATS_STREAMING_URL="http://defaultFissionAuthToken@$(kubectl -n $FISSION_NAMESPACE get svc nats-streaming -o jsonpath='{...ip}:{.spec.ports[0].port}')"

## Parameters used by some specific test cases

export PYTHON_RUNTIME_IMAGE=fission/python-env
export PYTHON_BUILDER_IMAGE=fission/python-builder
export GO_RUNTIME_IMAGE=fission/go-env
export GO_BUILDER_IMAGE=fission/go-builder
export JVM_RUNTIME_IMAGE=fission/jvm-env
export JVM_BUILDER_IMAGE=fission/jvm-builder


if [ $(uname -s) == 'Darwin' ]
then
    # gtimeout needs to be installed separately, do "brew install coreutils".
    timeout() {
        gtimeout "$@"
    }
    export -f timeout

    # gdate needs to be installed separately, do "brew install coreutils".
    date() {
        gdate "$@"
    }
    export -f date

    #brew install gnu-sed --with-default-names --> needed for gsed to work
    sed() {
      gsed "$@"
    }
    export -f sed

    log() {
        echo "$@"
    }
    export -f log

    export FISSION_ROUTER=$(kubectl -n fission get svc router -o jsonpath='{...ip}')
fi


if [[ $# -gt 0 ]]
then
  
  for var in "$@"
  do
    test_file=$(find $ROOT/test/tests -iname $var)
    run_test $test_file
  done

else 

  test_files=$(find $ROOT/test/tests -iname 'test_*.sh')

  for file in $test_files
  do
    run_test ${file}
  done
fi 
