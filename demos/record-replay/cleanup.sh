#!/bin/bash

fission fn delete --name hi-py
fission route delete --name $(fission route list|grep hi|cut -f1 -d' ')
fission recorder delete --name my-recorder
kubectl -n fission delete pod redis-0
