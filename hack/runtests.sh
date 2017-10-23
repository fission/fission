#!/bin/bash


if [ ! -f ${KUBECONFIG} ]
then
    unset KUBECONFIG
else
    K="kubectl --kubeconfig $KUBECONFIG"
    if $K get configmap ok-to-destroy
    then
	$K get function.fission.io -o name | cut -f2 -d'/' | xargs $K delete function.fission.io
	$K get environment.fission.io -o name | cut -f2 -d'/' | xargs $K delete environment.fission.io
	$K get httptrigger.fission.io -o name | cut -f2 -d'/' | xargs $K delete httptrigger.fission.io
    fi
fi

go test -v -i $(go list ./... | grep -v '/vendor/' | grep -v 'examples/go')

# The executor unit test only works with NodePort-type services for
# now. So disable it for our travis ci tests.
go test -v $(go list ./... | grep -v '/vendor/' | grep -v 'examples/go' | grep -v executor)
