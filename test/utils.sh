#!/usr/bin/env bash
#
# Utils for test scripts.
#
source $(dirname $BASH_SOURCE)/init_tools.sh

log() {
    echo `date +%Y/%m/%d:%H:%M:%S`" $1"
}

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

