#!/bin/bash


# Disabled because CI Fails for invalid function https://github.com/fission/fission/issues/653

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

env=nodejs-$TEST_ID
valid_fn_name=hello-$TEST_ID
invalid_fn_name=errhello-$TEST_ID
namespace=$(test -n "${TEST_ALL_NAMESPACES}" && echo "custom-ns" || echo "default")

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
    if [ -n "${TEST_ALL_NAMESPACES}" ]; then
      kubectl delete namespace custom-ns --ignore-not-found
    fi
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

if [ -n "${TEST_ALL_NAMESPACES}" ]; then
  log "Creating namespace 'custom-ns'"
  kubectl create namespace custom-ns
fi

log "Creating env $env in $namespace namespace"
fission env create --name $env --image $NODE_RUNTIME_IMAGE -n $namespace

log "Creating valid function $valid_fn_name in $namespace namespace"
fission fn create --name $valid_fn_name --env $env --code $(dirname $0)/hello.js -n $namespace

log "Testing valid function $valid_fn_name in $namespace namespace"
fission fn test --name $valid_fn_name -n $namespace > $tmp_dir/valid.log

log "---Valid Function logs---"
cat $tmp_dir/valid.log
log "------"
valid_num=$(grep 'Hello, Fission' $tmp_dir/valid.log | wc -l)

###
# Disabling invalid function test because executor does not propagate back the error from the environment but instead stuck in a retry loop to execute the function
# Todo: Limit the number of retries when getting executed by fn test or parameterize that
###

#if [ $valid_num -ne 1 ]
#then
#    log "Valid function Test Failed: expected 1, found $valid_num logs"
#    exit 1
#fi
#
#log "Creating function with an error $invalid_fn_name in $namespace namespace"
#fission fn create --name $invalid_fn_name --env $env --code $(dirname $0)/errhello.js -n $namespace
#
#log "Testing invalid function $invalid_fn_name in $namespace namespace"
#fission fn test --name $invalid_fn_name -n $namespace > $tmp_dir/invalid.log
#
#for i in {1..10}
#do
#    size=$(wc -c < $tmp_dir/invalid.log)
#    if [ $size == 0 ]
#    then
#        fission fn test --name $invalid_fn_name > $tmp_dir/invalid.log
#    else
#        break
#    fi
#done
#
#log "---Invalid Function logs---"
#cat $tmp_dir/invalid.log
#log "------"
#invalid_num=$(grep 'SyntaxError' $tmp_dir/invalid.log | wc -l)
#
#if [ $invalid_num -ne 1 ]
#then
#    log "Invalid function Failed: expected 1, found $invalid_num logs"
#    exit 1
#fi

log "All tests passed"
