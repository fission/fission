#!/bin/bash

set -euo pipefail

# This doesn't test fission, just the test framework. It ensures we
# have the right environment, that's all.

echo_log "Test test, please ignore."

echo_log $FISSION_URL
echo_log $FISSION_ROUTER
which fission
