#!/bin/bash

#
# Download kubectl, save kubeconfig, and ensure we can access the test cluster
#

set -e 

curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl
chmod +x ./kubectl
sudo mv ./kubectl /usr/local/bin/kubectl

mkdir ${HOME}/.kube
echo $KUBECONFIG_CONTENTS | base64 -D - > ${HOME}/.kube/config

kubectl version
