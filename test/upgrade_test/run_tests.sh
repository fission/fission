#!/bin/bash

set -e

RANDOM=124
ROOT=$(pwd)
REPO="docker.io/library"

generate_test_id() {
    echo $(((10000 + $RANDOM) % 99999))
}

# This will change for every new release
STABLE_VERSION=1.12.0

id=$(generate_test_id)
ns=f-$id
fns=f-func-$id
controllerNodeport=31234
routerServiceType=LoadBalancer

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
    kubectl get pods -A # For testing purpose
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
      echo "Success, function response received !!!"
      else
      echo "Failure, did not get a success reponse from function"
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


install_current_release () {
    set -x
    echo "List existing Helm charts..."
    helm list -A
    echo "Updating helm dependencies..."
    helm dependency update $ROOT/charts/fission-all
    sleep 2
    echo "Replacing CRDs..."
    kubectl replace -k crds/v1
    sleep 30

    IMAGE=fission-bundle
    FETCHER_IMAGE=fetcher
    BUILDER_IMAGE=builder
    #TAG=latest
    #helmVars=analytics=false,pruneInterval=60,routerServiceType=LoadBalancer,repository=$REPO,imageTag=latest,image=fission-bundle,fetcher.imageTag=latest,fetcher.image=fetcher 
    helm upgrade --namespace $ns fission $ROOT/charts/fission-all
    sleep 30
    kubectl get pods -A # For testing purpose
}


dump_system_info
install_stable_release
create_fission_objects
test_fission_objects
build_docker_images
kind_image_load
install_current_release
