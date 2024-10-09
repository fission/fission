#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

# test_env_podspec.sh - tests whether a user is able to add sidecar containers to a Fission environment deployment

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ENV=python-${TEST_ID}
RESOURCE_NS=default # Change to test-specific namespace once we support namespaced CRDs
FUNCTION_NS=${FUNCTION_NAMESPACE:-fission-function}
BUILDER_NS=${BUILDER_NAMESPACE:-fission-builder}
PYTHON_BUILDER_IMAGE=ghcr.io/fission/python-builder
PYTHON_RUNTIME_IMAGE=ghcr.io/fission/python-env

# fs
ENV_SPEC_FILE=${tmp_dir}/${ENV}.yaml

log_exec() {
    cmd=$@
    echo "> ${cmd}"
    ${cmd}
}

cleanup() {
    echo "previous response" $?
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi


# retry function adapted from:
# https://unix.stackexchange.com/questions/82598/how-do-i-write-a-retry-logic-in-script-to-keep-retrying-to-run-it-upto-5-times/82610
function retry {
  local n=1
  local max=10
  local delay=10 # pods take time to get ready
  while true; do
    "$@" && break || {
      if [[ ${n} -lt ${max} ]]; then
        ((n++))
        echo "Command '$@' failed. Attempt $n/$max:"
        sleep ${delay};
      else
        >&2 echo "The command has failed after $n attempts."
        exit 1;
      fi
    }
  done
}

# Deploy environment (using kubectl because the Fission cli does not support the podSpec arguments)
# Positive test
echo "Writing environment config to $ENV_SPEC_FILE"
cat > $ENV_SPEC_FILE <<- EOM
apiVersion: fission.io/v1
kind: Environment
metadata:
  name: ${ENV}
  namespace: ${RESOURCE_NS}
spec:
  builder:
    command: build
    container:
      name: builder
    image: ${PYTHON_BUILDER_IMAGE}
    podspec:
      containers:
      - name: builder
      initContainers:
      - name: init
        image: alpine
        command:
        - "sleep"
        - "1"
  runtime:
    container:
      name: ${ENV}
      resources: {}
    image: ${PYTHON_RUNTIME_IMAGE}
    podspec:
      containers:
      - name: ${ENV}
      initContainers:
      - name: init
        image: alpine
        command:
        - "sleep"
        - "1"
  version: 3
  poolsize: 1
EOM
log_exec kubectl -n ${RESOURCE_NS} apply -f ${ENV_SPEC_FILE}

timeout 90 bash -c "wait_for_builder $ENV"
log "environment is ready"

# Check if the initContainer status is completed in the builder env
status=0
if kubectl --namespace ${BUILDER_NS} get po -l envName=${ENV} -ojsonpath='{range .items[0]}{@.status.initContainerStatuses[0].state.terminated.reason}{end}' | grep Completed ; then
    log "InitContainer's status is correct."
else
    log "InitContainer's status is not correct"
    echo "--- Builder Env ---"
    kubectl --namespace ${BUILDER_NS} get deploy -l envName=go -ojson
    echo "--- End Builder Env ---"
    status=5
fi

# Check if the initContainer status is completed in the runtime env
if kubectl --namespace ${FUNCTION_NS} get po -l environmentName=${ENV} -ojsonpath='{range .items[0]}{@.status.initContainerStatuses[0].state.terminated.reason}{end}' | grep Completed ; then
    log "InitContainer's status is correct."
else
    log "InitContainer's status is not correct"
    echo "--- Runtime Env ---"
    kubectl --namespace ${FUNCTION_NS} get deploy -l environmentName=go -ojson
    echo "--- End Runtime Env ---"
    status=5
fi

if [ ${status} -eq 5 ] ; then
    exit ${status}
fi

# Deploy environment (using kubectl because the Fission cli does not support the podSpec arguments)
# Negative test
#
# Valid builder and runtime container names are `builder` and `envName` respectively. If any other name
# is used for them either in podSpec or container spec or both specs then `builder` and `runtime`
# deployments will not be created

NEW_ENV=python-new-${TEST_ID}
echo "Writing environment config to $ENV_SPEC_FILE"
cat > $ENV_SPEC_FILE <<- EOM
apiVersion: fission.io/v1
kind: Environment
metadata:
  name: ${NEW_ENV}
  namespace: ${RESOURCE_NS}
spec:
  builder:
    command: build
    container:
      name: builder
    image: ${PYTHON_BUILDER_IMAGE}
    podspec:
      containers:
      - name: builder-test
      initContainers:
      - name: init
        image: alpine
        command:
        - "sleep"
        - "1"
  runtime:
    container:
      name: ${NEW_ENV}
      resources: {}
    image: ${PYTHON_RUNTIME_IMAGE}
    podspec:
      containers:
      - name: runtime-test
      initContainers:
      - name: init
        image: alpine
        command:
        - "sleep"
        - "1"
  version: 3
  poolsize: 1
EOM
log_exec kubectl -n ${RESOURCE_NS} apply -f ${ENV_SPEC_FILE}

sleep 15
log "environment is created"

# Verify that no builder pod exists
status=0
if kubectl --namespace ${BUILDER_NS} get po -l envName=${NEW_ENV} | wc -l | grep 0 ; then
    log "Builder pod does not exist"
else
    log "Builder pod exists"
    echo "--- Builder Env ---"
    kubectl --namespace ${BUILDER_NS} get pod -l envName=${NEW_ENV} -ojson
    echo "--- End Builder Env ---"
    status=5
fi

# Verify that no runtime pod exists
if kubectl --namespace ${FUNCTION_NS} get po -l environmentName=${NEW_ENV} | wc -l | grep 0 ; then
    log "Runtime pod does not exist"
else
    log "Runtime pod exist"
    echo "--- Runtime Env ---"
    kubectl --namespace ${FUNCTION_NS} get po -l environmentName==${NEW_ENV} -ojson
    echo "--- End Runtime Env ---"
    status=5
fi

exit ${status}