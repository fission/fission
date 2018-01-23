#!/bin/bash

set -euo pipefail

# global variables
pkg=""
http_status=""
url=""
kubectl_pf_pid=""

cleanup() {
    if [ -e "test-deploy-pkg.zip" ]; then
        rm -rf test-deploy-pkg.zip test_dir
    fi
    if [ -e "/tmp/file" ]; then
        rm -rf /tmp/file
    fi
    if [ "$kubectl_pf_pid" != "" ]; then
        kill -9 $kubectl_pf_pid
    fi
}

check_return_status() {
    if [ $1 -ne 0 ]; then
        echo "Error"
        cleanup
        exit 1
    fi
}

create_archive() {
    echo "Creating an archive"
    mkdir test_dir
    if [ `uname` == "Darwin" ] ; then
        mkfile 512m test_dir/dynamically_generated_file
    else
        # Linux
        truncate -s 512M test_dir/dynamically_generated_file
    fi
    printf 'def main():\n    return "Hello, world!"' > test_dir/hello.py
    zip -jr test-deploy-pkg.zip test_dir/
    check_return_status $?
}

create_package() {
    echo "Creating package"
    pkg=$(fission package create --deploy "test-deploy-pkg.zip" --env python| cut -f2 -d' '| tr -d \')
    check_return_status $?
}

delete_package() {
    echo "Deleting package: $1"
    fission package delete --name $1
    check_return_status $?
}

get_archive_url_from_package() {
    echo "Getting archive URL from package: $1"
    url=`kubectl get package $1 -ojsonpath='{.spec.deployment.url}'`
    check_return_status $?
}

kubectl_port_forward() {
    controller_pod=`kubectl get pods --all-namespaces | grep "controller" | tr -s ' '| cut -d" " -f2`
    controller_ns=`kubectl get pods --all-namespaces | grep "controller" | tr -s ' '| cut -d" " -f1`
    remote_port=`kubectl get svc controller -n $controller_ns -ojsonpath='{.spec.ports[0].nodePort}'`
    local_port=35565
    echo "controller pod : $controller_pod, controller_ns: $controller_ns, remote_port : $remote_port"
    kubectl port-forward $controller_pod $local_port:$remote_port 2>&1 > /dev/null &
    kubectl_pf_pid=$!
    echo "kubectl port forward process id : $kubectl_pf_pid"
}

get_archive_from_storage() {
    http_status=`curl -sw "%{http_code}" $1 -o /tmp/file`
}

#1. create an archives with large files such that total size of archive is > 256KB
#2. create 2 pkgs referencing those archives
#3. delete both the packages
#4. sleep for 30 seconds
#5. verify archives are not recycled . this handles the case where archives are just created but not referenced by pkgs yet.
#6. sleep for one minute
#7. now verify that both get deleted.
#8. cleanup
main() {
    # create a huge archive
    create_archive
    echo "created archive test-deploy-pkg.zip"

    # create packages with the huge archive
    create_package
    pkg_1=$pkg
    get_archive_url_from_package $pkg_1
    url_1=$url
    echo "pkg: $pkg_1, archive_url : $url_1"

    create_package
    pkg_2=$pkg
    get_archive_url_from_package $pkg_2
    url_2=$url
    echo "pkg: $pkg_2, archive_url : $url_2"

    # delete packages
    delete_package $pkg_1
    delete_package $pkg_2
    echo "deleted packages : $pkg_1 $pkg_2"

    # to find out if archive is present or absent on the storage, we can curl the archive url
    # very soon, the controller (proxying for storage http requests) will not have a public IP.
    # so, in any case, do a port forward of controller pod before executing curl get of archive url.
    kubectl_port_forward

    # curl on the archive url
    get_archive_from_storage $url_1
    echo "http_status for $url_1 : $http_status"
    if [ "$http_status" -ne "200" ]; then
        echo "Archive $url_1 absent on storage, while expected to be present"
        cleanup
        exit 1
    fi

    # curl on the archive url
    get_archive_from_storage $url_2
    echo "http_status for $url_2 : $http_status"
    if [ "$http_status" -ne "200" ]; then
        echo "Archive $url_2 absent on storage, while expected to be present"
        cleanup
        exit 1
    fi

    # archivePruner is set to run every minute for test. In production, its set to run every hour.
    echo "waiting for packages to get recycled"
    sleep 120

    # curl on the archive url
    get_archive_from_storage $url_1
    echo "http_status for $url_1 : $http_status"
    if [ "$http_status" -ne "404" ]; then
        echo "Archive $url_1 should have been recycled, but curl returned $http_status, while expected status is 404."
        cleanup
        exit 1
    fi

    # curl on the archive url
    get_archive_from_storage $url_2
    echo "http_status for $url_2 : $http_status"
    if [ "$http_status" -ne "404" ]; then
        echo "Archive $url_2 should have been recycled, but curl returned $http_status, while expected status is 404."
        cleanup
        exit 1
    fi

    echo "Test archive pruner PASSED"
    cleanup
}

main