#!/bin/bash

#
# Download kubectl, save kubeconfig, and ensure we can access the test cluster
#

set -e 

# If we don't have gcloud credentials, bail out of these tests.
if [ -z "$FISSION_CI_SERVICE_ACCOUNT" ]
then
    echo "Skipping tests, no cluster credentials"
    exit 0
fi

K8SCLI_DIR=$HOME/k8scli

if [ ! -d $K8SCLI_DIR ]
then
    mkdir -p $K8SCLI_DIR
fi

# Get kubectl
if [ ! -f $K8SCLI_DIR/kubectl ]
then
   curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl
   chmod +x ./kubectl
   mv kubectl $K8SCLI_DIR/kubectl
fi

# Get helm
if [ ! -f $K8SCLI_DIR/helm ]
then
    curl -LO https://storage.googleapis.com/kubernetes-helm/helm-v2.7.2-linux-amd64.tar.gz
    tar xzvf helm-*.tar.gz
    mv linux-amd64/helm $K8SCLI_DIR/helm
fi

mkdir ${HOME}/.kube

# echo $KUBECONFIG_CONTENTS | base64 -D - > ${HOME}/.kube/config
# kubectl version

# gcloud stuff
# https://stackoverflow.com/questions/38762590/how-to-install-google-cloud-sdk-on-travis

if [ ! -d "${HOME}/google-cloud-sdk/bin" ]
then
    rm -rf $HOME/google-cloud-sdk
    export CLOUDSDK_CORE_DISABLE_PROMPTS=1
    curl https://sdk.cloud.google.com | bash
fi

# gcloud command
export PATH=${HOME}/google-cloud-sdk/bin:${PATH}

# ensure we have the gcloud binary
gcloud version

# get gcloud credentials
echo $FISSION_CI_SERVICE_ACCOUNT | base64 -d - > ${HOME}/gcloud-service-key.json
gcloud auth activate-service-account --key-file ${HOME}/gcloud-service-key.json

# get kube config
gcloud container clusters get-credentials fission-ci-1 --zone us-central1-a --project fission-ci

# remove gcloud creds
unset FISSION_CI_SERVICE_ACCOUNT
rm ${HOME}/gcloud-service-key.json

# does it work?

if [ ! -f ${HOME}/.kube/config ]
then
    echo "Missing kubeconfig"
    exit 1
fi

kubectl get node
