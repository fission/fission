#!/bin/bash

set -euo pipefail

if [ ! -f ${HOME}/.kube/config ]
then
    echo "Skipping end to end tests, no cluster credentials"
    exit 0
fi

source ./test/test_utils.sh

echo "source test_utils done"

dump_system_info

export FUNCTION_NAMESPACE=fission-function
export FISSION_NAMESPACE=fission
export FISSION_ROUTER=127.0.0.1:8888
export NODE_RUNTIME_IMAGE=fission/node-env-12.16
export NODE_BUILDER_IMAGE=fission/node-builder-12.16
export PYTHON_RUNTIME_IMAGE=fission/python-env
export PYTHON_BUILDER_IMAGE=fission/python-builder
export GO_RUNTIME_IMAGE=fission/go-env-1.12
export GO_BUILDER_IMAGE=fission/go-builder-1.12 
export JVM_RUNTIME_IMAGE=fission/jvm-env
export JVM_BUILDER_IMAGE=fission/jvm-builder
export JVM_JERSEY_RUNTIME_IMAGE=fission/jvm-jersey-env
export JVM_JERSEY_BUILDER_IMAGE=fission/jvm-jersey-builder
export TS_RUNTIME_IMAGE=fission/tensorflow-serving-env
export CONTROLLER_IP=127.0.0.1:8889
export FISSION_NATS_STREAMING_URL=http://defaultFissionAuthToken@127.0.0.1:8890

echo "Pulling env and builder images"
docker pull -q $NODE_RUNTIME_IMAGE && kind load docker-image $NODE_RUNTIME_IMAGE --name kind
docker pull -q $NODE_BUILDER_IMAGE && kind load docker-image $NODE_BUILDER_IMAGE --name kind
docker system prune -a -f > /dev/null
docker pull -q $PYTHON_RUNTIME_IMAGE && kind load docker-image $PYTHON_RUNTIME_IMAGE --name kind
docker pull -q $PYTHON_BUILDER_IMAGE && kind load docker-image $PYTHON_BUILDER_IMAGE --name kind
docker pull -q $JVM_RUNTIME_IMAGE && kind load docker-image $JVM_RUNTIME_IMAGE --name kind
docker pull -q $JVM_BUILDER_IMAGE && kind load docker-image $JVM_BUILDER_IMAGE --name kind
docker system prune -a -f > /dev/null
docker pull -q $JVM_JERSEY_RUNTIME_IMAGE && kind load docker-image $JVM_JERSEY_RUNTIME_IMAGE --name kind
docker pull -q $JVM_JERSEY_BUILDER_IMAGE && kind load docker-image $JVM_JERSEY_BUILDER_IMAGE --name kind
docker pull -q $GO_RUNTIME_IMAGE && kind load docker-image $GO_RUNTIME_IMAGE --name kind
docker pull -q $GO_BUILDER_IMAGE && kind load docker-image $GO_BUILDER_IMAGE --name kind
docker system prune -a -f > /dev/null
docker pull -q $TS_RUNTIME_IMAGE && kind load docker-image $TS_RUNTIME_IMAGE --name kind
echo "Successfully pull env and builder images"

# run tests without newdeploy in parallel.

export FAILURES=0
main() {
    set +e
    export TIMEOUT=1200  # 20 minutes per test
    # run tests without newdeploy in parallel.
    export JOBS=6
    source $ROOT/test/run_test.sh \
        $ROOT/test/tests/test_canary.sh \
        $ROOT/test/tests/test_fn_update/test_idle_objects_reaper.sh \
        $ROOT/test/tests/mqtrigger/kafka/test_kafka.sh \
        $ROOT/test/tests/test_annotations.sh \
        $ROOT/test/tests/test_archive_pruner.sh \
        $ROOT/test/tests/test_backend_poolmgr.sh \
        $ROOT/test/tests/test_buildermgr.sh \
        $ROOT/test/tests/test_env_vars.sh \
        $ROOT/test/tests/test_environments/test_python_env.sh \
        $ROOT/test/tests/test_function_test/test_fn_test.sh \
        $ROOT/test/tests/test_function_update.sh \
        $ROOT/test/tests/test_ingress.sh \
        $ROOT/test/tests/test_internal_routes.sh \
        $ROOT/test/tests/test_logging/test_function_logs.sh \
        $ROOT/test/tests/test_node_hello_http.sh \
        $ROOT/test/tests/test_package_command.sh \
        $ROOT/test/tests/test_package_checksum.sh \
        $ROOT/test/tests/test_pass.sh \
        $ROOT/test/tests/test_specs/test_spec.sh \
        $ROOT/test/tests/test_specs/test_spec_multifile.sh \
        $ROOT/test/tests/test_specs/test_spec_merge/test_spec_merge.sh \
        $ROOT/test/tests/test_specs/test_spec_archive/test_spec_archive.sh \
        $ROOT/test/tests/test_environments/test_tensorflow_serving_env.sh \
        $ROOT/test/tests/test_environments/test_go_env.sh \
        $ROOT/test/tests/mqtrigger/nats/test_mqtrigger.sh \
        $ROOT/test/tests/mqtrigger/nats/test_mqtrigger_error.sh \
        $ROOT/test/tests/test_huge_response/test_huge_response.sh \
        $ROOT/test/tests/test_kubectl/test_kubectl.sh \
        $ROOT/test/tests/websocket/test_ws.sh

    export JOBS=3
    source $ROOT/test/run_test.sh \
        $ROOT/test/tests/test_backend_newdeploy.sh \
        $ROOT/test/tests/test_fn_update/test_scale_change.sh \
        $ROOT/test/tests/test_secret_cfgmap/test_secret_cfgmap.sh \
        $ROOT/test/tests/test_environments/test_java_builder.sh \
        $ROOT/test/tests/test_environments/test_java_env.sh \
        $ROOT/test/tests/test_environments/test_nodejs_env.sh \
        $ROOT/test/tests/test_fn_update/test_configmap_update.sh \
        $ROOT/test/tests/test_fn_update/test_env_update.sh \
        $ROOT/test/tests/test_obj_create_in_diff_ns.sh \
        $ROOT/test/tests/test_fn_update/test_resource_change.sh \
        $ROOT/test/tests/test_fn_update/test_secret_update.sh \
        $ROOT/test/tests/test_fn_update/test_nd_pkg_update.sh \
        $ROOT/test/tests/test_fn_update/test_poolmgr_nd.sh  

    set -e

    # dump test logs
    # TODO: the idx does not match seq number in recap.
    idx=1
    log_files=$(find test/logs/ -name '*.log')

    for log_file in $log_files; do
        test_name=${log_file#test/logs/}
        # travis_fold_start run_test.$idx $test_name
        echo "========== start $test_name =========="
        cat $log_file
        echo "========== end $test_name =========="
        # travis_fold_end run_test.$idx
        idx=$((idx+1))
    done
}

main

echo "Total Failures" $FAILURES
if [[ $FAILURES != '0' ]]; then
    exit 1
fi