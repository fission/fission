#!/bin/bash
set -eu

ns="f-ns"
ROOT=$(pwd)
PREV_STABLE_VERSION=1.13.1
HELM_VARS_LATEST_RELEASE="helmVars=repository=docker.io/library,image=fission-bundle,pullPolicy=IfNotPresent,imageTag=latest,fetcher.image=docker.io/library/fetcher,fetcher.imageTag=latest,postInstallReportImage=reporter,preUpgradeChecksImage=preupgradechecks"

dump_system_info() {
    echo "System Info"
    go version
    docker version
    kubectl version
    helm version
}

install_stable_release() {
    helm repo add fission-charts https://fission.github.io/fission-charts/
    helm repo update

    echo "Creating namespace $ns"
    kubectl create ns $ns

    echo "Creating CRDs"
    kubectl create -k "github.com/fission/fission/crds/v1?ref=$PREV_STABLE_VERSION"

    echo "Installing Fission $PREV_STABLE_VERSION"
    helm install --version $PREV_STABLE_VERSION--namespace $ns fission fission-charts/fission-all

    echo "Download fission cli $PREV_STABLE_VERSION"
    curl -Lo fission https://github.com/fission/fission/releases/download/$PREV_STABLE_VERSION/fission-$PREV_STABLE_VERSION-linux-amd64 && chmod +x fission && sudo mv fission /usr/local/bin/
    sleep 10

    fission version
}

create_fission_objects() {
    echo "-----------------#########################################--------------------"
    echo "                   Preparing for fission object creation"
    echo "-----------------#########################################--------------------"
    echo "Creating function environment."
    if fission env create --name nodejs --image fission/node-env:latest; then
        echo "Successfully created function environment"
    else
        echo "Environemnt creation failed"
    fi

    echo "Creating function"
    curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
    if fission function create --name hello --env nodejs --code hello.js; then
        echo "Successfully created function"
    else
        echo "Function creation failed"
        exit
    fi
}

test_fission_objects() {
    echo "-----------------###############################--------------------"
    echo "                   Running fission object tests"
    echo "-----------------###############################--------------------"
    if fission function test --name hello; then
        echo "----------------------**********************-------------------------"
        echo "                           Test success"
        echo "----------------------**********************-------------------------"
    else
        echo "----------------------**********************-------------------------"
        echo "                            Test failed"
        echo "----------------------**********************-------------------------"
    fi
}

build_docker_images() {
    echo "Building new docker images"
    docker build -t fission-bundle -f cmd/fission-bundle/Dockerfile.fission-bundle .
    docker build -t fetcher -f cmd/fetcher/Dockerfile.fission-fetcher .
    docker build -t builder -f cmd/builder/Dockerfile.fission-builder .
    docker build -t reporter -f cmd/reporter/Dockerfile.reporter .
    docker build -t preupgradechecks -f cmd/preupgradechecks/Dockerfile.fission-preupgradechecks .
}

kind_image_load() {
    echo "Loading Docker images into Kind cluster."
    kind load docker-image fission-bundle --name kind
    kind load docker-image fetcher --name kind
    kind load docker-image builder --name kind
    kind load docker-image reporter --name kind
    kind load docker-image preupgradechecks --name kind
}

install_fission_cli() {
    echo "Installing new Fission cli"
    make install-fission-cli
}

install_current_release() {
    echo "Running Fission upgrade"
    helm dependency update "$ROOT"/charts/fission-all
    make update-crds
    helm upgrade --namespace $ns --set $HELM_VARS_LATEST_RELEASE fission "$ROOT"/charts/fission-all
}

"$@"
