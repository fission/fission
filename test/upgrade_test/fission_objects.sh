#!/bin/bash
set -e

getVersion() {
    echo $(git rev-parse HEAD)
}

getDate() {
    echo $(date -u +'%Y-%m-%dT%H:%M:%SZ')
}

getGitCommit() {
    echo $(git rev-parse HEAD)
}

generate_test_id() {
    echo $(((10000 + $RANDOM) % 99999))
}

dump_system_info() {
    echo "System Info"
    go version
    docker version
    kubectl version
    helm version
}

install_stable_release () {
    echo "Creating namespace $ns"
    kubectl create ns $ns
    helm install \
    --namespace $ns \
    --name-template fission \
    https://github.com/fission/fission/releases/download/${STABLE_VERSION}/fission-all-${STABLE_VERSION}.tgz

    mkdir temp && cd temp && curl -Lo fission https://github.com/fission/fission/releases/download/${STABLE_VERSION}/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/ && cd .. && rm -rf temp
    sleep 30
}

create_fission_objects () {
    fission env create --name nodejs --image fission/node-env:latest
    curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
    fission function create --name hello --env nodejs --code hello.js
    if [ $? == 0 ]
      then
      echo "Success, function created successfully"
      else
      echo "Function creation failed"
      exit
    fi
    sleep 2
 }

test_fission_objects () {
    fission function test --name hello
    if [ $? == 0 ]
      then
      echo "Test success"
      else
      echo "Test failed"
    fi
}

build_docker_images () {
    docker build -t fission-bundle -f cmd/fission-bundle/Dockerfile.fission-bundle .
    docker build -t fetcher -f cmd/fetcher/Dockerfile.fission-fetcher .
    docker build -t builder -f cmd/builder/Dockerfile.fission-builder .
    docker build -t reporter -f cmd/reporter/Dockerfile.reporter .
}

kind_image_load () {
    echo "Loading Docker images into Kind cluster...."
    kind load docker-image fission-bundle --name kind
    kind load docker-image fetcher --name kind
    kind load docker-image builder --name kind
    kind load docker-image reporter --name kind
    sleep 5
    echo "checking image load status..."
    docker exec -t kind-control-plane crictl images
}

install_fission_cli () {
    go build -ldflags \
    "-X github.com/fission/fission/pkg/info.GitCommit=$(getGitCommit) \
    -X github.com/fission/fission/pkg/info.BuildDate=$(getDate) \
    -X github.com/fission/fission/pkg/info.Version=$(getVersion)" \
    -o fission ./cmd/fission-cli/main.go
    chmod +x fission && sudo mv fission /usr/local/bin/
    fission version
}

install_current_release () {
    helm dependency update $ROOT/charts/fission-all
    kubectl replace -k crds/v1
    sleep 30
    helm upgrade --namespace $ns --set $helmVars=$HELM_VARS fission $ROOT/charts/fission-all
}