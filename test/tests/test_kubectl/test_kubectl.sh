#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

ROOT=$(dirname $0)/../../..

# cd test/tests/test_kubectl
specs=test/tests/test_kubectl/spec-yaml

cleanup() {
    kubectl delete -f specs -R || true
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

name="go-spec-kubectl"
pkgName="go-b4bbb0e0-2d93-47f0-8c4e-eea644eec2a9"

# cleanup first
cleanup

# apply environment & function
kubectl apply -f specs -R

# wait for build to finish
timeout 90 bash -c "wait_for_builder $name"
timeout 90 bash -c "waitBuildExpectedStatus $pkgName failed"

sed -i 's/gogo/go/g' specs/function-go.yaml

# before we enable "/status" this should be failed.
kubectl apply -f specs/function-go.yaml
timeout 90 bash -c "waitBuildExpectedStatus $pkgName failed"

kubectl replace -f specs/function-go.yaml
timeout 90 bash -c "waitBuild $pkgName"

fission fn test --name $name

log "Test PASSED"
