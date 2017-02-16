#!/bin/sh

set -euo pipefail

generate_test_id() {
    echo $(date|md5|cut -c1-6)
}

helm_install_fission() {    
    id=$1
    image=$2
    imageTag=$3
    controllerNodeport=$4
    routerNodeport=$5

    ns=f-$id
    fns=f-func-$id

    helmVars=image=$image,imageTag=$imageTag,namespace=$ns,functionNamespace=$fns,controllerPort=$controllerNodeport,routerPort=$routerNodeport

    echo "Installing fission"
    helm install		\
	 --wait			\
	 --name $id		\
	 --set $helmVars	\
	 ../charts/fission
}

helm_uninstall_fission() {
    echo "Uninstalling fission"
    helm delete $1
}

run_all_tests() {
    echo "Here is where our tests will go"
}

install_and_test() {
    image=$1
    imageTag=$2

    controllerPort=31234
    routerPort=31235
    
    id=$(generate_test_id)
    trap "helm_uninstall_fission $id" EXIT
    helm_install_fission $id $image $imageTag $controllerPort $routerPort 

    run_all_tests $controllerPort $routerPort
}


if [ $# -lt 2 ]
then
    echo "Usage: test.sh [image] [imageTag]"
    exit 1
fi

install_and_test $1 $2
