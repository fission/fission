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


# export FISSION_URL=http://$(kubectl -n fission get svc controller -o jsonpath='{...ip}')
# export FISSION_ROUTER=$(kubectl -n fission get svc router -o jsonpath='{...ip}')
# export FISSION_NATS_STREAMING_URL="http://defaultFissionAuthToken@$(kubectl -n fission get svc nats-streaming -o jsonpath='{...ip}:{.spec.ports[0].port}')"

# export NODE_RUNTIME_IMAGE=fission/node-env-12.16:1.11.0
# run tests without newdeploy in parallel.


export JOBS=6
test/run_test.sh \
    test/tests/test_canary.sh \
#     test/tests/test_fn_update/test_idle_objects_reaper.sh \
#     test/tests/mqtrigger/kafka/test_kafka.sh \
#     test/tests/test_annotations.sh \
#     test/tests/test_archive_pruner.sh \
#     test/tests/test_backend_poolmgr.sh \
#     test/tests/test_buildermgr.sh \
#     test/tests/test_env_vars.sh \
#     test/tests/test_environments/test_python_env.sh \
#     test/tests/test_function_test/test_fn_test.sh \
#     test/tests/test_function_update.sh \
#     test/tests/test_ingress.sh \
#     test/tests/test_internal_routes.sh \
#     test/tests/test_logging/test_function_logs.sh \
#     test/tests/test_node_hello_http.sh \
#     test/tests/test_package_command.sh \
#     test/tests/test_package_checksum.sh \
#     test/tests/test_pass.sh \
#     test/tests/test_specs/test_spec.sh \
#     test/tests/test_specs/test_spec_multifile.sh \
#     test/tests/test_specs/test_spec_merge/test_spec_merge.sh \
#     test/tests/test_specs/test_spec_archive/test_spec_archive.sh \
#     test/tests/test_environments/test_tensorflow_serving_env.sh \
#     test/tests/test_environments/test_go_env.sh \
#     test/tests/mqtrigger/nats/test_mqtrigger.sh \
#     test/tests/mqtrigger/nats/test_mqtrigger_error.sh \
#     test/tests/test_huge_response/test_huge_response.sh \
#     test/tests/test_kubectl/test_kubectl.sh
# FAILURES=$?

# export JOBS=3
# test/run_test.sh \
#     test/tests/test_backend_newdeploy.sh \
#     test/tests/test_environments/test_java_builder.sh \
#     test/tests/test_environments/test_java_env.sh \
#     test/tests/test_environments/test_nodejs_env.sh \
#     test/tests/test_fn_update/test_configmap_update.sh \
#     test/tests/test_fn_update/test_env_update.sh \
#     test/tests/test_fn_update/test_nd_pkg_update.sh \
#     test/tests/test_fn_update/test_poolmgr_nd.sh \
#     test/tests/test_fn_update/test_resource_change.sh \
#     test/tests/test_fn_update/test_scale_change.sh \
#     test/tests/test_fn_update/test_secret_update.sh \
#     test/tests/test_obj_create_in_diff_ns.sh \
#     test/tests/test_secret_cfgmap/test_secret_cfgmap.sh
# FAILURES=$((FAILURES+$?))
# set -e

# dump test logs
# TODO: the idx does not match seq number in recap.
idx=1
log_files=$(find test/logs/ -name '*.log')
echo "Log files" $log_files
echo "Log files" $log_files
echo "Log files" $log_files
echo "Log files" $log_files

for log_file in $log_files; do
    test_name=${log_file#test/logs/}
    travis_fold_start run_test.$idx $test_name
    echo "========== start $test_name =========="
    cat $log_file
    echo "========== end $test_name =========="
    travis_fold_end run_test.$idx
    idx=$((idx+1))
done
