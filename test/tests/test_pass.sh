#!/bin/bash

set -euo pipefail
# This doesn't test fission, just the test framework. It ensures we
# have the right environment, that's all.

log "Test test, please ignore."

log $FISSION_NATS_STREAMING_URL
log $FISSION_ROUTER
which fission
