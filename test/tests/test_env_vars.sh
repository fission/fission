#!/usr/bin/env bash
#test:disabled
set -euo pipefail

# test_env_vars.sh - tests whether a user is able to add environment variables to a Fission environment deployment

TEST_ID=$(date +%s)
ENV=python-${TEST_ID}
FN=foo-${TEST_ID}
RESOURCE_NS=default # Change to test-specific namespace once we support namespaced CRDs
FUNCTION_NS=${FUNCTION_NAMESPACE:-fission-function}
BUILDER_NS=fission-builder

# fs
TEST_DIR=/tmp/${TEST_ID}
ENV_SPEC_FILE=${TEST_DIR}/${ENV}.yaml
FN_FILE=${TEST_DIR}/${FN}.yaml

log_exec() {
    cmd=$@
    echo "> ${cmd}"
    ${cmd}
}

cleanup() {
    log "Cleaning up..."
    kubectl -n ${RESOURCE_NS} delete environment/${ENV} || true
    rm -rf ${TEST_DIR}

}

cleanup
if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

if ! stat ${TEST_DIR} >/dev/null 2>&1 ; then
    mkdir ${TEST_DIR}
fi

getPodName() {
    NS=$1
    POD=$2
    # find pod is ready to serve
    JSONPATH="{range .items[*]}{'\n'}{@.metadata.name}:{range @.status.conditions[*]}{@.type}={@.status};{end}{end}"
    kubectl -n ${NS} get po -o jsonpath="$JSONPATH" \
        | grep "Ready=True" \
        | grep ${POD} \
        | head -n 1 \
        | cut -f1 -d":"
}

# retry function adapted from:
# https://unix.stackexchange.com/questions/82598/how-do-i-write-a-retry-logic-in-script-to-keep-retrying-to-run-it-upto-5-times/82610
function retry {
  local n=1
  local max=5
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

# Deploy environment (using kubectl because the Fission cli does not support the container arguments)
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
    image: gcr.io/fission-ci/python-env-builder:test
    container:
      env:
      - name: TEST_BUILDER_ENV_KEY
        value: "TEST_BUILDER_ENV_VAR"

  runtime:
    image: gcr.io/fission-ci/python-env:test
    container:
      env:
      - name: TEST_RUNTIME_ENV_KEY
        value: "TEST_RUNTIME_ENV_VAR"
  version: 2
  poolsize: 1
EOM
log_exec kubectl -n ${RESOURCE_NS} apply -f ${ENV_SPEC_FILE}

sleep 15
# Wait for runtime and build env to be deployed
retry getPodName ${FUNCTION_NS} ${ENV} | grep '.\+'
runtimePod=$(getPodName ${FUNCTION_NS} ${ENV})
echo "function pod: ${runtimePod}."
retry getPodName ${BUILDER_NS} ${ENV} | grep '.\+'
buildPod=$(getPodName ${BUILDER_NS} ${ENV})
echo "builder pod: ${buildPod}."

# Ensure pods are running/ready
log "Waiting for ${FUNCTION_NS} ${ENV} to be available..."
echo "> kubectl -n ${FUNCTION_NS} exec ${runtimePod} -c ${ENV} env"
retry kubectl -n ${FUNCTION_NS} exec ${runtimePod} -c ${ENV} env > /dev/null
log "Runtime pod ready."

log "Waiting for ${BUILDER_NS} ${ENV} to be available..."
echo "> kubectl -n ${BUILDER_NS} exec ${buildPod} -c builder env"
retry kubectl -n ${BUILDER_NS} exec ${buildPod} -c builder env > /dev/null
log "Builder pod ready."

# Check if the env is set in the runtime
status=0
if kubectl -n ${FUNCTION_NS} exec ${runtimePod} -c ${ENV}  env | grep TEST_RUNTIME_ENV_KEY=TEST_RUNTIME_ENV_VAR ; then
    log "Runtime env is correct."
else
    log "Runtime does not contain expected env var: TEST_RUNTIME_ENV_KEY=TEST_RUNTIME_ENV_VAR"
    echo "--- Runtime Env ---"
    kubectl -n ${FUNCTION_NS} exec ${runtimePod} -c ${ENV} env || true
    echo "--- End Runtime Env ---"
    status=5
fi

# Check if the env is set in the builder
if kubectl -n ${BUILDER_NS} exec ${buildPod} -c builder env | grep TEST_BUILDER_ENV_KEY=TEST_BUILDER_ENV_VAR ; then
    log "Builder env is correct."
else
    log "Builder does not contain expected env var: TEST_BUILDER_ENV_KEY=TEST_BUILDER_ENV_VAR"
    echo "--- Builder Env ---"
    kubectl -n ${BUILDER_NS} exec ${buildPod} -c builder env || true
    echo "--- End Builder Env ---"
    status=5
fi
exit ${status}
