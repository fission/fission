#!/usr/bin/env bash
#
# This is a helper script to run test in parallel and collect logs.
# Usage:
#       ./run_test.sh                       Run all tests.
#       ./run_test.sh [test_file ...]       Run specific tests.
#
# Environments:
#       JOBS        The number of concurrent jobs. (default: 1)
#       LOG_DIR     Log directory path. (default: $ROOT/test/logs)
#
set -euo pipefail
source $(dirname $BASH_SOURCE)/init_tools.sh

ROOT=$(readlink -f $(dirname $0)/..)
LOG_DIR=${LOG_DIR:-$ROOT/test/logs}
JOB=${JOB:-1}

main() {
    if [ $# -eq 0 ]; then
        args=$(find_executable $ROOT/test/tests -iname 'test_*')
    else
        args="$@"
    fi

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
            echo "WARNING: the test is marked disabled: $relative_path"
            continue
        fi

        # make sure the log dir exists.
        mkdir -p $(dirname $log_path)

        # remove common path for readability
        test_files="$test_files ${absolute_path#$PWD/}"
        log_files="$log_files ${log_path#$PWD/}"
    done

    parallel \
        --joblog $LOG_DIR/_recap \
        --jobs $JOB \
        bash -c '{1} > {2} 2>&1' \
        ::: $test_files :::+ $log_files \
        || true
    cat $LOG_DIR/_recap
}

main "$@"
