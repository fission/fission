#!/bin/bash

kubectl delete canaryconfig canary-1
kubectl delete httptrigger route-canary

fission fn delete --name func-v1
fission fn delete --name func-v2
fission package delete --orphan

# bug in canary config cache when you re-create a config with the same name, restart controller
kubectl -n fission get pod -l application=fission-api -o name | xargs -n1 kubectl -n fission delete
