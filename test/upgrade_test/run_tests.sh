#!/bin/bash

#set -e

RANDOM=124
ROOT=$(pwd)
REPO="docker.io/library"
STABLE_VERSION=1.12.0

source $ROOT/test/upgrade_test/fission_objects.sh

id=$(generate_test_id)
ns=f-$id
fns=f-func-$id
controllerNodeport=31234
routerServiceType=LoadBalancer


#Invoking fuctions
dump_system_info
install_stable_release
create_fission_objects
test_fission_objects
build_docker_images
kind_image_load
install_current_release
