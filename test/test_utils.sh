#!/bin/bash

#
# Test runner. Shell scripts that build fission CLI and server, push a
# docker image to GCR, deploy it on a cluster, and run tests against
# that deployment.
#

set -euo pipefail

ROOT=$(dirname $0)/..

export TEST_REPORT=""

report_msg() {
    TEST_REPORT="$TEST_REPORT\n$1"
}
report_test_passed() {
    report_msg "--- PASSED $1"
}
report_test_failed() {
    report_msg "*** FAILED $1"
}
show_test_report() {
    echo "------\n$TEST_REPORT\n------"
}

helm_setup() {
    helm init
    # wait for tiller ready
    while true; do
      kubectl --namespace kube-system get pod|grep tiller|grep Running
      if [[ $? -eq 0 ]]; then
          break
      fi
      sleep 1
    done
}
export -f helm_setup

gcloud_login() {
    KEY=${HOME}/gcloud-service-key.json
    if [ ! -f $KEY ]
    then
	echo $FISSION_CI_SERVICE_ACCOUNT | base64 -d - > $KEY
    fi

    gcloud auth activate-service-account --key-file $KEY
}

build_and_push_fission_bundle() {
    image_tag=$1

    pushd $ROOT/fission-bundle
    ./build.sh
    docker build -t $image_tag .

    gcloud_login

    gcloud docker -- push $image_tag
    popd
}

build_and_push_fetcher() {
    image_tag=$1

    pushd $ROOT/environments/fetcher/cmd
    ./build.sh
    docker build -t $image_tag .

    gcloud_login

    gcloud docker -- push $image_tag
    popd
}


build_and_push_builder() {
    image_tag=$1

    pushd $ROOT/builder/cmd
    ./build.sh
    docker build -t $image_tag .

    gcloud_login

    gcloud docker -- push $image_tag
    popd
}

build_and_push_fluentd(){
    image_tag=$1

    pushd $ROOT/logger/fluentd
    docker build -t $image_tag .

    gcloud_login

    gcloud docker -- push $image_tag
    popd

}

build_and_push_env_runtime() {
    env=$1
    image_tag=$2

    pushd $ROOT/environments/$env/
    docker build -t $image_tag .

    gcloud_login

    gcloud docker -- push $image_tag
    popd
}

build_and_push_env_builder() {
    env=$1
    image_tag=$2
    builder_image=$3

    pushd $ROOT/environments/$env/builder

    docker build -t $image_tag --build-arg BUILDER_IMAGE=${builder_image} .

    gcloud_login

    gcloud docker -- push $image_tag
    popd
}

build_fission_cli() {
    pushd $ROOT/fission
    go build .
    popd
}

clean_tpr_crd_resources() {
    # clean tpr & crd resources to avoid testing error (ex. no kind "HttptriggerList" is registered for version "fission.io/v1")
    # thirdpartyresources part should be removed after kubernetes test cluster is upgrade to 1.8+
    kubectl --namespace default get thirdpartyresources| grep -v NAME| grep "fission.io"| awk '{print $1}'|xargs -I@ bash -c "kubectl --namespace default delete thirdpartyresources @" || true
    kubectl --namespace default get crd| grep -v NAME| grep "fission.io"| awk '{print $1}'|xargs -I@ bash -c "kubectl --namespace default delete crd @"  || true
}

generate_test_id() {
    echo $(date|md5sum|cut -c1-6)
}

helm_install_fission() {
    id=$1
    image=$2
    imageTag=$3
    fetcherImage=$4
    fetcherImageTag=$5
    controllerNodeport=$6
    routerNodeport=$7
    fluentdImage=$8

    ns=f-$id
    fns=f-func-$id

    helmVars=image=$image,imageTag=$imageTag,fetcherImage=$fetcherImage,fetcherImageTag=$fetcherImageTag,functionNamespace=$fns,controllerPort=$controllerNodeport,routerPort=$routerNodeport,pullPolicy=Always,analytics=false,logger.fluentdImage=$fluentdImage

    timeout 30 helm_setup

    echo "Deleting old releases"
    helm list -q|xargs helm_uninstall_fission

    echo "Installing fission"
    helm install		\
	 --wait			\
	 --timeout 600	        \
	 --name $id		\
	 --set $helmVars	\
	 --namespace $ns        \
	 --debug                \
	 $ROOT/charts/fission-all

    helm list
}

wait_for_service() {
    id=$1
    svc=$2

    ns=f-$id
    while true
    do
	ip=$(kubectl -n $ns get svc $svc -o jsonpath='{...ip}')
	if [ ! -z $ip ]
	then
	    break
	fi
	echo Waiting for service $svc...
	sleep 1
    done
}

wait_for_services() {
    id=$1

    wait_for_service $id controller
    wait_for_service $id router
}

helm_uninstall_fission() {
    if [ ! -z ${FISSION_TEST_SKIP_DELETE:+} ]
    then
	echo "Fission uninstallation skipped"
	return
    fi
    echo "Uninstalling fission"
    helm delete --purge $1
}
export -f helm_uninstall_fission

set_environment() {
    id=$1
    ns=f-$id

    export FISSION_URL=http://$(kubectl -n $ns get svc controller -o jsonpath='{...ip}')
    export FISSION_ROUTER=$(kubectl -n $ns get svc router -o jsonpath='{...ip}')

    # set path to include cli
    export PATH=$ROOT/fission:$PATH
}

dump_function_pod_logs() {
    ns=$1
    fns=$2

    functionPods=$(kubectl -n $fns get pod -o name -l functionName)
    for p in $functionPods
    do
	echo "--- function pod logs $p ---"
	containers=$(kubectl -n $fns get $p -o jsonpath={.spec.containers[*].name})
	for c in $containers
	do
	    echo "--- function pod logs $p: container $c ---"
	    kubectl -n $fns logs $p $c
	    echo "--- end function pod logs $p: container $c ---"
	done
	echo "--- end function pod logs $p ---"
    done
}

dump_fission_logs() {
    ns=$1
    fns=$2
    component=$3

    echo --- $component logs ---
    kubectl -n $ns get pod -o name  | grep $component | xargs kubectl -n $ns logs
    echo --- end $component logs ---
}

dump_fission_crd() {
    type=$1
    echo --- All objects of type $type ---
    kubectl --all-namespaces=true get $type -o yaml
    echo --- End objects of type $type ---
}

dump_fission_crds() {
    dump_fission_crd environments.fission.io
    dump_fission_crd functions.fission.io
    dump_fission_crd httptriggers.fission.io
    dump_fission_crd kuberneteswatchtriggers.fission.io
    dump_fission_crd messagequeuetriggers.fission.io
    dump_fission_crd packages.fission.io
    dump_fission_crd timetriggers.fission.io
}

dump_env_pods() {
    fns=$1

    echo --- All environment pods ---
    kubectl -n $fns get pod -o yaml
    echo --- End environment pods ---
}

dump_all_fission_resources() {
    ns=$1

    echo "--- All objects in the fission namespace $ns ---"
    kubectl -n $ns get all
    echo "--- End objects in the fission namespace $ns ---"
}

dump_system_info() {
    echo "--- System Info ---"
    go version
    docker version
    kubectl version
    helm version
    echo "--- End System Info ---"
}

dump_logs() {
    id=$1

    ns=f-$id
    fns=f-func-$id

    dump_all_fission_resources $ns
    dump_env_pods $fns
    dump_fission_logs $ns $fns controller
    dump_fission_logs $ns $fns router
    dump_fission_logs $ns $fns buildermgr
    dump_fission_logs $ns $fns executor
    dump_function_pod_logs $ns $fns
    dump_fission_crds
}

export FAILURES=0

run_all_tests() {
    id=$1

    export FISSION_NAMESPACE=f-$id
    export FUNCTION_NAMESPACE=f-func-$id

    pushd $ROOT/test/tests
    test_files=$(find $(pwd) -iname 'test_*.sh')

    for file in $test_files
    do
	testname=${file#$ROOT/test/tests}
	testpath=$file
	echo ------- Running $testname -------
	pushd $(dirname $testpath)
	if $testpath
	then
	    echo SUCCESS: $testname
	    report_test_passed $testname
	else
	    echo FAILED: $testname
	    export FAILURES=$(($FAILURES+1))
	    report_test_failed $testname
	fi
	popd
    done
    popd
}

install_and_test() {
    image=$1
    imageTag=$2
    fetcherImage=$3
    fetcherImageTag=$4
    fluentdImage=$5
    fluentdImageTag=$6

    controllerPort=31234
    routerPort=31235

    clean_tpr_crd_resources

    id=$(generate_test_id)
    trap "helm_uninstall_fission $id" EXIT
    if ! helm_install_fission $id $image $imageTag $fetcherImage $fetcherImageTag $controllerPort $routerPort $fluentdImage:$fluentdImageTag
    then
	dump_logs $id
	exit 1
    fi

    wait_for_services $id
    set_environment $id

    run_all_tests $id

    dump_logs $id

    show_test_report

    if [ $FAILURES -ne 0 ]
    then
	exit 1
    fi
}


# if [ $# -lt 2 ]
# then
#     echo "Usage: test.sh [image] [imageTag]"
#     exit 1
# fi
# install_and_test $1 $2
