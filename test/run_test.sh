#!/usr/bin/env bash
#
# This is a helper script to run test in parallel and collect logs.
# Usage:
#       ./run_test.sh                       Run all tests.
#       ./run_test.sh [test_file ...]       Run specific tests.
#
# Environments:
#       LOG_DIR     Log directory path. (default: $ROOT/test/logs)
#       JOBS        The number of concurrent jobs. (default: 1)
#       TIMEOUT     Timeout for each job. (default: 0 (no timeout))
#
set -euo pipefail
source $(dirname $BASH_SOURCE)/init_tools.sh

ROOT=$(readlink -f $(dirname $0)/..)
LOG_DIR=${LOG_DIR:-$ROOT/test/logs}
JOBS=${JOBS:-1}
TIMEOUT=${TIMEOUT:-0}

main() {
    if [ $# -eq 0 ]; then
        args=$(find_executable $ROOT/test/tests -iname 'test_*')
    else
        args="$@"
    fi

    num_skip=0
    mkdir -p $LOG_DIR
    test_files=""
    log_files=""
    for arg in $args; do
        if [ ! -f $arg ]; then
            echo "WARNING: file not found: $arg"
            continue
        fi

        absolute_path=$(readlink -f $arg)
        relative_path=${absolute_path#$ROOT/test/tests/}
        log_path=$LOG_DIR/${relative_path}.log

        if grep -q "^#test:disabled" $arg; then
            echo "INFO: the test is marked disabled: $relative_path"
            num_skip=$((num_skip+1))
            continue
        fi

        # make sure the log dir exists.
        mkdir -p $(dirname $log_path)

        # remove common path for readability
        test_files="$test_files ${absolute_path#$PWD/}"
        log_files="$log_files ${log_path#$PWD/}"
    done

    start_time=$(date +%s)
    parallel \
        --joblog - \
        --jobs $JOBS \
        --timeout $TIMEOUT \
        bash -c '{1} > {2} 2>&1' \
        ::: $test_files :::+ $log_files \
        | tee $LOG_DIR/_recap \
        || true
    end_time=$(date +%s)

    # Get the Exitval in _recap to find if any test failed.
    num_total=$(cat $LOG_DIR/_recap | wc -l)
    num_total=$((num_total - 1))    # don't count header
    num_fail=$(cat $LOG_DIR/_recap | awk 'NR>1 && $7!=0 {print $0}' | wc -l | tr -d ' ')
    num_pass=$((num_total - num_fail))
    time=$((end_time - start_time))
    echo ============================================================
    echo "PASS: $num_pass    SKIP: $num_skip    FAIL: $num_fail    TIME: ${time}s"
    return $num_fail
}

main "$@"
