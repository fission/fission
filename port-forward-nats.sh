#!/bin/bash

svc=$1
if [ -z "$svc" ]
then
    svc=nats-streaming
fi

port=$2
if [ -z "$port" ]
then
    port=8888
fi

kubectl get pods -l svc=$svc -o name --namespace default | \
        sed 's/^.*\///' | \
        xargs -I{} kubectl port-forward {} $port:$port -n default &
