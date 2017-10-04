#!/usr/bin/env bash

#
# deploy.sh - (Almost) automatic setup of a Fission Workflows deployment
#

# Configs
FISSION_VERSION=0.3.0-rc
FISSION_WORKFLOWS_VERSION=0.1.0

echo "Fission Workflows Deploy Script v1"

# Check if kubectl is installed
if ! command -v kubectl >/dev/null 2>&1; then
    echo "kubectl is not installed"
    exit 1;
fi

# Check if minikube is installed
if ! command -v minikube >/dev/null 2>&1 ; then
    echo "minikube is not installed."
    exit 1;
fi

# Check if helm is installed
if ! command -v helm >/dev/null 2>&1 ; then
    echo "helm is not installed."
    exit 1;
fi

# Ensure that minikube is running
if ! minikube ip >/dev/null 2>&1 ; then
    echo "Starting minikube cluster..."
    minikube start
fi

# Install helm on cluster
if ! helm list >/dev/null 2>&1 ; then
    echo "Installing helm..."
    kubectl -n kube-system create sa tiller

    kubectl create clusterrolebinding tiller --clusterrole cluster-admin --serviceaccount=kube-system:tiller

    helm init --service-account tiller

    printf "Waiting for Helm"
    until helm list >/dev/null 2>&1
    do
      printf "."
      sleep 3
    done
    printf "\n"
fi

# Output debug logs
echo "---------- Debug ----------"
minikube version
printf "K8S "
kubectl version --client --short
printf "K8S "
kubectl version --short | grep Server
printf "Helm "
helm version --short -c
printf "Helm "
helm version --short -s
echo "Fission: ${FISSION_VERSION}"
echo "Fission Workflows: ${FISSION_WORKFLOWS_VERSION}"
echo "---------------------------"

# Ensure that fission-charts helm repo is added
if ! helm repo list | grep fission-charts >/dev/null 2>&1 ; then
    echo "Setting up fission-charts Helm repo..."
    helm repo add fission-charts https://fission.github.io/fission-charts/
    helm repo update
fi

# Install Fission
if ! fission fn list >/dev/null 2>&1 ; then
    echo "Installing Fission..."
    helm install --namespace fission --set serviceType=NodePort -n fission-all fission-charts/fission-all --wait --version ${FISSION_VERSION}
fi

# Install Fission Workflows
if ! fission env get --name workflow >/dev/null 2>&1 ; then
    echo "Installing Fission Workflows..."
    if [[ -z "${FISSION_WORKFLOWS_VERSION// }" ]] ; then
        helm install -n fission-workflows fission-charts/fission-workflows --wait --version ${FISSION_WORKFLOWS_VERSION}
    else
        helm install -n fission-workflows fission-charts/fission-workflows --wait
    fi
fi
