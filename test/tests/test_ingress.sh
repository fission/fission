#!/bin/bash
set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

ROOT=$(dirname $0)/../..

relativeUrl="/itest-$TEST_ID"
functionName="hellotest-$TEST_ID"
hostName="test-$TEST_ID.com"

cleanup() {
    clean_resource_by_id $TEST_ID
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

log "Creating route for URL $relativeUrl"
route_name=$(fission route create --url $relativeUrl --function $functionName --createingress| grep trigger| cut -d" " -f 2|cut -d"'" -f 2)

log "Route $route_name created"

sleep 5

log "Ingresses matching this trigger:"
kubectl get ing -l 'functionName='$functionName',triggerName='$route_name --all-namespaces -o=json

log "Verifying to route value in ingress"
actual_route=$(kubectl -n fission get ing -l 'functionName='$functionName',triggerName='$route_name --all-namespaces -o=jsonpath='{.items[0].spec.rules[0].http.paths[0].path}')

if [ $actual_route != $relativeUrl ]
then
    log "Provided route and route in ingress don't match"
    exit 1
fi

log "Modifying the route by adding host"
fission route update --name $route_name --host $hostName --function $functionName

sleep 2

actual_host=$(kubectl get ing -l 'functionName='$functionName',triggerName='$route_name --all-namespaces -o=jsonpath='{.items[0].spec.rules[0].host}')

if [ $hostName != $actual_host ]
then
    log "Provided host and host in ingress don't match"
    exit 1
fi

log "Test PASSED"
