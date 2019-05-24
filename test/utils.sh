#!/usr/bin/env bash
#
# Utils for test scripts.
#
source $(dirname $BASH_SOURCE)/init_tools.sh

log() {
    echo `date +%Y/%m/%d:%H:%M:%S`" $@"
}
export -f log

generate_test_id() {
    echo $(cat /dev/urandom | tr -dc 'a-z' | fold -w 8 | head -n 1)
}

clean_resource_by_id() {
    test_id=$1
    KUBECTL="kubectl --namespace default"
    set +e

    crds=$($KUBECTL get crd | grep "fission.io" | awk '{print $1}')
    crds="$crds configmaps secrets"
    for crd in $crds; do
        $KUBECTL get $crd -o name | grep $test_id | xargs --no-run-if-empty $KUBECTL delete
    done

    pkg_list=$(fission package list | grep $test_id | awk '{print $1}')
    for pkg in $pkg_list; do
        fission package delete --name $pkg
    done

    route_list=$(fission route list | grep $test_id | awk '{print $1}')
    for route in $route_list; do
        fission route delete --name $route
    done
    set -e
}

test_fn() {
    # Doing an HTTP GET on the function's route
    # Checking for valid response
    url="http://$FISSION_ROUTER/$1"
    expect=$2

    set +e
    while true; do
        log "test_fn: call curl"
        resp=$(curl --silent --show-error "$url")
        status_code=$?
        if [ $status_code -ne 0 ]; then
            log "test_fn: curl failed ($status_code). Retrying ..."
            sleep 1
            continue
        fi
        if ! (echo $resp | grep "$expect" > /dev/null); then
            log "test_fn: resp = '$resp'    expect = '$expect'"
            log "test_fn: expected string not found. Retrying ..."
            sleep 1
            continue
        fi
        break
    done
    set -e
}
export -f test_fn

## Common env parameters
export FISSION_NAMESPACE=${FISSION_NAMESPACE:-fission}
export FUNCTION_NAMESPACE=${FUNCTION_NAMESPACE:-fission-function}

export FISSION_ROUTER=$(kubectl -n $FISSION_NAMESPACE get svc router -o jsonpath='{...ip}')
export FISSION_NATS_STREAMING_URL="http://defaultFissionAuthToken@$(kubectl -n $FISSION_NAMESPACE get svc nats-streaming -o jsonpath='{...ip}:{.spec.ports[0].port}')"

## Parameters used by some specific test cases
export PYTHON_RUNTIME_IMAGE=${PYTHON_RUNTIME_IMAGE:-fission/python-env}
export PYTHON_BUILDER_IMAGE=${PYTHON_BUILDER_IMAGE:-fission/python-builder}
export GO_RUNTIME_IMAGE=${GO_RUNTIME_IMAGE:-fission/go-env}
export GO_BUILDER_IMAGE=${GO_BUILDER_IMAGE:-fission/go-builder}
export JVM_RUNTIME_IMAGE=${JVM_RUNTIME_IMAGE:-fission/jvm-env}
export JVM_BUILDER_IMAGE=${JVM_BUILDER_IMAGE:-fission/jvm-builder}
export NODE_RUNTIME_IMAGE=${NODE_RUNTIME_IMAGE:-fission/node-env}

