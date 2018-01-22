#!/bin/bash

set -euo pipefail

#1. create 2 archives with large files such that total size of archive is > 256KB
#2. create 2 pkgs referencing those archives
#3. delete both the packages
#4. sleep for 30 seconds
#5. verify archives are not recycled . this handles the case where archives are just created but not referenced by pkgs yet.
#6. sleep for one minute
#7. now verify that both get deleted.
#8. delete huge file, zip files

pkg_1 = ""
pkg_2 = ""
create_packages() {
    echo "Creating a package with deploy archive"
    mkdir test_dir
    touch test_dir/__init__.py
    if [ `uname` == "Darwin" ] ; then
        mkfile 512m test_dir/dynamically_generated_file
    else
        # Linux
        truncate -s 512M test_dir/dynamically_generated_file
    fi
    printf 'def main():\n    return "Hello, world!"' > test_dir/hello.py
    for (( c=1; c<=2; c++ ))
    do
        zip -jr test-deploy-pkg-$c.zip test_dir/
        pkg=$(fission package create --deploy "test-deploy-pkg-$c.zip" --env python| cut -f2 -d' '| tr -d \')
        if [ $c == 1 ]; then
                pkg_1=$pkg
        else
                pkg_2=$pkg
        fi
    done
    echo "Packages created : $pkg_1 $pkg_2"
    rm -rf test_dir test-deploy-pkg*
}

delete_packages() {
    echo "Deleting packages: $pkg_1 $pkg_2"
    fission package delete --name $pkg_1
    if [ $? -ne 0 ]; then
        echo "Error deleting package: $pkg_1"
    fi
    fission package delete --name $pkg_2
    if [ $? -ne 0 ]; then
        echo "Error deleting package: $pkg_2"
    fi
}

http_status = ""
get_archive_from_storage() {
    # TODO : need to use fission cli to get HTTP status
    url = `kubectl get $1 -ojsonpath='{.spec.deployment.url}'`
    echo "url : $url"
    http_status = `curl $url -sI -XGET | grep -i "http" | cut -d" " -f2`
}

main () {
    create_packages
    echo "created packages : $pkg_1 $pkg_2"

    sleep 30

    delete_packages
    echo "deleted packages : $pkg_1 $pkg_2"

    get_archive_from_storage $pkg_1
    echo "http_status : $http_status"
    if [ "$http_status" -ne "200" ]; then
        echo "Something went wrong. Test failed."
        cleanup
        exit 1
    fi

    get_archive_from_storage $pkg_2
    echo "http_status : $http_status"
    if [ "$http_status" -ne "200" ]; then
        echo "Something went wrong. Test failed."
        cleanup
        exit 1
    fi

    # give it some time to get recycled
    sleep 120

    get_archive_from_storage $pkg_1
    echo "http_status : $http_status"
    if [ "$http_status" -ne "404" ]; then
        echo "Something went wrong. Test failed."
        cleanup
        exit 1
    fi

    get_archive_from_storage $pkg_2
    echo "http_status : $http_status"
    if [ "$http_status" -ne "404" ]; then
        echo "Something went wrong. Test failed."
        cleanup
        exit 1
    fi

    echo "Test archive pruner PASSED"
}

main