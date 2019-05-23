#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi


# This doesn't test fission, just the test framework. It ensures we
# have the right environment, that's all.

log "Test test, please ignore."

log $FISSION_NATS_STREAMING_URL
log $FISSION_ROUTER
which fission
log "Test PASSED"
