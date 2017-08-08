#!/bin/bash

export NODE_IP=$(kubectl get node -o jsonpath='{$..addresses[?(@.type=="ExternalIP")].address}')

# Hacks for poolmgr unit tests.
export TEST_FETCHER_URL=http://$NODE_IP:30001
export TEST_SPECIALIZE_URL=http://$NODE_IP:30002/specialize

go test -v -i $(go list ./... | grep -v '/vendor/' | grep -v 'examples/go')
go test -v $(go list ./... | grep -v '/vendor/' | grep -v 'examples/go')
