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

# run tests without newdeploy in parallel.

export FAILURES=0
main() {
    set +e
    export TIMEOUT=1000  # 15 minutes per test 
    # run tests without newdeploy in parallel.
    export JOBS=6
    source $ROOT/test/run_test.sh

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