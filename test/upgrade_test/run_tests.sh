#!/bin/bash

set -e

ROOT=$(pwd)
REPO="docker.io/library"
STABLE_VERSION=1.12.0
HELM_VARS="helmVars=repository=docker.io/library,image=fission-bundle,pullPolicy=IfNotPresent,imageTag=latest,fetcher.image=docker.io/library/fetcher,fetcher.imageTag=latest,postInstallReportImage=reporter" 

source $ROOT/test/upgrade_test/fission_objects.sh

id=$(generate_test_id)
ns=f-$id
fns=f-func-$id

#Invoking fuctions
dump_system_info
install_stable_release
create_fission_objects
test_fission_objects
build_docker_images
kind_image_load
install_fission_cli
install_current_release
