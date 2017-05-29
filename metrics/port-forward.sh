#!/bin/bash

app=$1
if [ -z "$app" ]
then
    app=prometheus
fi

port=$2
if [ -z "$port" ]
then
    port=9090
fi

kubectl get pods -l app=$app -o name --namespace fission | \
        sed 's/^.*\///' | \
        xargs -I{} kubectl port-forward {} $port:$port --namespace fission