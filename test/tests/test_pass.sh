#!/bin/sh

set -euo pipefail

# This doesn't test fission, just the test framework. It ensures we
# have the right environment, that's all.

echo "Test test, please ignore."

echo $FISSION_URL
echo $FISSION_ROUTER
which fission
