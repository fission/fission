#!/bin/bash
#
# Make sure the Kind local registry works as expected.

set -ex

cd $(dirname $0)

docker pull busybox
docker tag busybox localhost:5000/busybox
docker push localhost:5000/busybox
kubectl delete -f pod.yaml --ignore-not-found
n=0; until ((n >= 60)); do kubectl -n fission get serviceaccount default -o name && break; n=$((n + 1)); sleep 1; done; ((n < 60))
kubectl create -f pod.yaml
kubectl wait --for=condition=ready pod/kind-test --timeout=60s
