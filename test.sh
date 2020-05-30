#!/bin/bash
#
# Make sure the Kind local registry works as expected.

set -ex

cd $(dirname $0)

docker pull busybox
docker tag busybox localhost:5000/busybox
docker push localhost:5000/busybox
kubectl delete -f pod.yaml --ignore-not-found
kubectl create -f pod.yaml
kubectl wait --for=condition=ready pod/kind-test --timeout=60s
