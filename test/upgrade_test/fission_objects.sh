#!/bin/bash
set -eu

ns="f-ns"
ROOT=$(pwd)
REPO="docker.io/library"
PREV_STABLE_VERSION=1.12.0
HELM_VARS="helmVars=repository=docker.io/library,image=fission-bundle,pullPolicy=IfNotPresent,imageTag=latest,fetcher.image=docker.io/library/fetcher,fetcher.imageTag=latest,postInstallReportImage=reporter,preUpgradeChecksImage=preupgradechecks" 

getVersion () {
    echo $(git rev-parse HEAD)
}

getDate () {
    echo $(date -u +'%Y-%m-%dT%H:%M:%SZ')
}

getGitCommit () {
    echo $(git rev-parse HEAD)
}

dump_system_info () {
    echo "System Info"
    go version
    docker version
    kubectl version
    helm version
}

install_stable_release () {
    echo "Creating namespace $ns"
    kubectl create ns $ns
    echo "Installing Fission $PREV_STABLE_VERSION"
    helm install \
    --namespace $ns \
    --name-template fission \
    https://github.com/fission/fission/releases/download/${PREV_STABLE_VERSION}/fission-all-${PREV_STABLE_VERSION}.tgz
    mkdir temp && cd temp && curl -Lo fission https://github.com/fission/fission/releases/download/${PREV_STABLE_VERSION}/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/ && cd .. && rm -rf temp
    sleep 10
 }

create_fission_objects () {
    echo "Creating Fission objects"
    fission env create --name nodejs --image fission/node-env:latest
    sleep 5
    curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
    if fission function create --name hello --env nodejs --code hello.js
      then
      echo "Success, function created successfully"
      else
      echo "Function creation failed"
      exit
    fi
    sleep 2
 }

test_fission_objects () {
    echo "list running fission functions"
    fission fn list
    echo "Testing Fission objects."
    helm list -A
    kubectl get pods -A
    if fission function test --name hello
      then
      echo "----------------------**********************-------------------------"
      echo "                           Test success"
      echo "----------------------**********************-------------------------"
      else
      echo "----------------------**********************-------------------------"
      echo "                            Test failed"
      echo "----------------------**********************-------------------------"
    fi
}

build_docker_images () {
    echo "Building new docker images"
    docker build -t fission-bundle -f cmd/fission-bundle/Dockerfile.fission-bundle .
    docker build -t fetcher -f cmd/fetcher/Dockerfile.fission-fetcher .
    docker build -t builder -f cmd/builder/Dockerfile.fission-builder .
    docker build -t reporter -f cmd/reporter/Dockerfile.reporter .
    docker build -t preupgradechecks -f cmd/preupgradechecks/Dockerfile.fission-preupgradechecks .
}

kind_image_load () {
    echo "Loading Docker images into Kind cluster."
    kind load docker-image fission-bundle --name kind
    kind load docker-image fetcher --name kind
    kind load docker-image builder --name kind
    kind load docker-image reporter --name kind
    kind load docker-image preupgradechecks --name kind
 }

install_fission_cli () {
    echo "Installing new Fission cli"
    go build -ldflags \
    "-X github.com/fission/fission/pkg/info.GitCommit=$(getGitCommit) \
    -X github.com/fission/fission/pkg/info.BuildDate=$(getDate) \
    -X github.com/fission/fission/pkg/info.Version=$(getVersion)" \
    -o fission ./cmd/fission-cli/main.go
    chmod +x fission && sudo mv fission /usr/local/bin/
}

install_current_release () {
    echo "Running Fission upgrade"
    helm dependency update $ROOT/charts/fission-all
    kubectl replace -k crds/v1
    helm upgrade --namespace $ns --set $HELM_VARS fission $ROOT/charts/fission-all
    sleep 5
}

"$@"