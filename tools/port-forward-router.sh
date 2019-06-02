#!/bin/bash

namespace=$1
if [ -z "$namespace" ]
then
    namespace=fission
fi

svc=$2
if [ -z "$svc" ]
then
    svc=router
fi

port=$3
if [ -z "$port" ]
then
    port=8888
fi


kubectl get pods -l svc=$svc -o name --namespace $namespace | \
        sed 's/^.*\///' | \
        xargs -I{} kubectl port-forward {} $port:8888 -n $namespace &
