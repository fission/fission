#!/bin/bash

# The poolmgr unit test only works with NodePort-type services for
# now. So disable it for our travis ci tests.

if [ ! -f ${KUBECONFIG} ]
then
    unset KUBECONFIG
fi

go test -v -i $(go list ./... | grep -v '/vendor/' | grep -v 'examples/go')
go test -v $(go list ./... | grep -v '/vendor/' | grep -v 'examples/go' | grep -v poolmgr)
