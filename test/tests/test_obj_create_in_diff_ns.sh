#!/bin/bash


# we may not need this to run as a pre-check-in test for every PR. but only once in a while to ensure nothing's broken.

set -euo pipefail

id=""
ROOT=$(dirname $0)/../..

final_cleanup() {
    rm -rf testDir1/ || true
    kubectl delete ns "ns1-$id" "ns2-$id" &
}

trap final_cleanup EXIT

cleanup() {
    [[ -n "${1+x}"  && -n "${2+x}" ]]; fission env delete --name $1 --envns $2 || true
    [[ -n "${3+x}"  && -n "${4+x}" ]]; fission fn delete --name $3 --fns $4 || true
    [[ -n "${5+x}"  && -n "${6+x}" ]]; fission pkg delete --name $5 --pkgns $6 || true
    [[ -n "${7+x}"  && -n "${8+x}" ]]; fission route delete --name $7 --triggerns $8 || true
}

create_python_source_code() {
    mkdir testDir1
    printf 'def main():\n    return "Hello, world!"' > testDir1/hello.py
}

verify_pod_ns() {
    kubectl get deployment -n $2 | grep $1
}

verify_function_pod_ns() {
    function_label=$1
    env_ns=$2

    kubectl get pods -n $env_ns -L functionName| grep $function_label
}


verify_obj_in_ns() {
    obj_kind=$1
    obj_name=$2
    obj_ns=$3

    kubectl get $obj_kind $obj_name -n $obj_ns
}

get_pkg_name_from_func() {
    pkg=`kubectl get function $1 -n $2 -o jsonpath='{.spec.package.packageref.name}'`
    echo "$pkg"
}

# since we havent deleted the funcs created previously in new_deploy_mgr tests,
# just curl on the funcs and verifying http response code should be enough
internal_route_test_2() {
    http_status=`curl -sw "%{http_code}" http://$FISSION_ROUTER/fission-function/func5 -o /tmp/file`
    echo "http_status : $http_status"
    [[ "$http_status" -eq "200" ]]
}

internal_route_test_1() {
    ns="ns2-$id"
    http_status=`curl -sw "%{http_code}" http://$FISSION_ROUTER/fission-function/$ns/func4 -o /tmp/file`
    echo "http_status : $http_status"
    [[ "$http_status" -eq "200" ]]
}

new_deploy_mgr_and_internal_route_test_2() {
    log "Starting new_deploy_mgr_and_internal_route_test_1 with env in default ns"
    fission env create --name python --image fission/python-env
    fission fn create --name func5 --env python --code testDir1/hello.py --minscale 1 --maxscale 4 --executortype newdeploy
    sleep 15

    # function is loaded in $FISSION_NAMESPACE because func object was created in default ns
    verify_function_pod_ns func5 "$FUNCTION_NAMESPACE" || (log "func func5 not specialized in $FUNCTION_NAMESPACE" &&
    cleanup python "default" func5 "default" "" "" "" "" && exit 1)
    internal_route_test_2 || ( log "internal route test for http://$FISSION_ROUTER/fission-function/func5 returned http_status: $http_status" &&
    cleanup python "default" func5 "default" "" "" "" "" && exit 1)

    cleanup python "default" func5 "default" "" "" "" ""
}

new_deploy_mgr_and_internal_route_test_1() {
    log "Starting new_deploy_mgr_and_internal_route_test_1 with env and fn in different ns"
    fission env create --name python --image fission/python-env --envns "ns1-$id"
    fission fn create --name func4 --fns "ns2-$id" --env python --envns "ns1-$id" --code testDir1/hello.py --minscale 1 --maxscale 4 --executortype newdeploy
    sleep 15

    # note that this test is diff from pool_mgr test because here function is loaded in func ns and not in env ns
    verify_function_pod_ns func4 "ns2-$id" || (log "func func4 not specialized in ns2-$id" &&
    cleanup python "ns1-$id" func4 "ns2-$id" "" "" "" "" && exit 1)
    internal_route_test_1 || (log "internal route test for http://$FISSION_ROUTER/fission-function/ns2-$id/func4 returned http_status: $http_status" &&
    cleanup python "ns1-$id" func4 "ns2-$id" "" "" "" "" && exit 1)

    cleanup python "ns1-$id" func4 "ns2-$id" "" "" "" ""
}

builder_mgr_test_2() {
    log "Starting builder_mgr_test_2 with env in default ns"
    fission env create --name python-builder-env --builder fission/python-builder --image fission/python-env
    sleep 180

    # verify the env builder pod came up in fission-builder and env runtime pod came up in fission-function ns
    verify_pod_ns python-builder-env fission-builder || (log "python-builder-env builder env not found in fission-builder ns" &&
    cleanup python-builder-env "default" "" "" "" "" "" "" && exit 1)
    verify_pod_ns python-builder-env $FUNCTION_NAMESPACE || (log "python-builder-env runtime env not found in fission-function ns" &&
    cleanup python-builder-env "default" "" "" "" "" "" "" && exit 1)

    zip -jr src-pkg.zip $ROOT/examples/python/sourcepkg/
    pkg=$(fission package create --src src-pkg.zip --env python-builder-env --buildcmd "./build.sh" --pkgns "ns2-$id"| cut -f2 -d' '| tr -d \')
    sleep 60
    fission fn create --name func4 --fns "ns2-$id" --pkg $pkg --entrypoint "user.main"
    ht=$(fission route create --function func4 --fns "ns2-$id" --url /func4 --method GET | cut -f2 -d' '| tr -d \')

    # get the function loaded into a pod
    sleep 10
    response=$(curl http://$FISSION_ROUTER/func4)
    echo $response
    echo $response | grep -i "a: 1 b: {c: 3, d: 4}" || (log "response a: 1 b: {c: 3, d: 4} not received" &&
    cleanup python-builder-env "default" func4 "ns2-$id" "$pkg" "ns2-$id" $ht "ns2-$id"  && exit 1)


    # verify the function specialized pod is in fission-function. This also verifies builder ran successfully and in fission-builder
    verify_function_pod_ns func4 $FUNCTION_NAMESPACE  || (log "func func4 not specialized in $FUNCTION_NAMESPACE " &&
    cleanup python-builder-env "default" func4 "ns2-$id" "$pkg" "ns2-$id" $ht "ns2-$id"  && exit 1)

    cleanup python-builder-env "default" func4 "ns2-$id" "$pkg" "ns2-$id" $ht "ns2-$id"
}

builder_mgr_test_1() {
    log "Starting builder_mgr_test_1 with env and fn in different ns"
    fission env create --name python-builder-env --envns "ns1-$id" --builder fission/python-builder --image fission/python-env
    # we need to wait sufficiently for env pods to be up
    sleep 180

    zip -jr src-pkg.zip $ROOT/examples/python/sourcepkg/
    pkg=$(fission package create --src src-pkg.zip --env python-builder-env --envns "ns1-$id" --buildcmd "./build.sh" --pkgns "ns2-$id"| cut -f2 -d' '| tr -d \')
    sleep 60
    fission fn create --name func3 --fns "ns2-$id" --pkg $pkg --entrypoint "user.main"
    ht=$(fission route create --function func3 --fns "ns2-$id" --url /func3 --method GET | cut -f2 -d' '| tr -d \')

    # get the function loaded into a pod
    sleep 10
    response=$(curl http://$FISSION_ROUTER/func3)
    echo "response : $response"
    echo $response | grep -i "a: 1 b: {c: 3, d: 4}" || (log "response a: 1 b: {c: 3, d: 4} not received" &&
    cleanup python-builder-env "ns1-$id" func3 "ns2-$id" "$pkg" "ns2-$id" $ht "ns2-$id"  && exit 1)

    # verify the function specialized pod is in ns1-$id. This also verifies builder ran successfully and in ns1-$id
    verify_function_pod_ns func3 "ns1-$id" || (log "func func3 not specialized in ns1-$id" &&
    cleanup python-builder-env "ns1-$id" func3 "ns2-$id" "$pkg" "ns2-$id" $ht "ns2-$id" && exit 1)

    cleanup python-builder-env "ns1-$id" func3 "ns2-$id" "$pkg" "ns2-$id" $ht "ns2-$id"
}

pool_mgr_test_2() {
    log "Starting pool_mgr_test_2 with env in default ns"
    fission env delete --name python || true
    fission env create --name python --image fission/python-env
    fission fn create --name func2 --fns "ns2-$id" --env python --code testDir1/hello.py
    ht=$(fission route create --function func2 --fns "ns2-$id" --url /func2 | cut -f2 -d' '| tr -d \')

    # verify that env object is created in default ns when envns option is absent with fission env create command
    verify_obj_in_ns environment python default|| (log "env python not found in default ns" &&
    cleanup python "default" func2 "ns2-$id" "" "" $ht "ns2-$id" && exit 1)

    sleep 3
    # get the function loaded into a pod
    response=$(curl http://$FISSION_ROUTER/func2)
    echo $response | grep "Hello" || (log "response Hello not received" &&
    cleanup python "default" func2 "ns2-$id" "" "" $ht "ns2-$id" && exit 1)

    # note that the env pod is created in the $FUNCTION_NAMESPACE ns though the env object is created in default ns
    # so even if the function is created in a different ns, they will be loaded in the $FUNCTION_NAMESPACE.
    verify_function_pod_ns func2 $FUNCTION_NAMESPACE || (log "func func2 not specialized in $FUNCTION_NAMESPACE" &&
    cleanup python "default" func2 "ns2-$id" "" "" $ht "ns2-$id" && exit 1)

    cleanup python "default" func2 "ns2-$id" "" "" $ht "ns2-$id"
}

pool_mgr_test_1() {
    log "Starting pool_mgr_test_1 with env and fn in different ns"
    fission env create --name python --image fission/python-env --envns "ns1-$id"
    fission fn create --name func1 --fns "ns2-$id" --env python --envns "ns1-$id" --code testDir1/hello.py
    ht=$(fission route create --function func1 --fns "ns2-$id" --url /func1 | cut -f2 -d' '| tr -d \')

    # verify that fission objects are created in the expected namespaces
    verify_obj_in_ns environment python "ns1-$id" || (log "env python not found in ns1-$id" &&
    cleanup python "ns1-$id" && exit 1)
    verify_obj_in_ns "function" func1 "ns2-$id" || (log "function func1 not found in ns2-$id" &&
    cleanup python "ns1-$id" func1 "ns2-$id" && exit 1)
    pkg=$( get_pkg_name_from_func func1 "ns2-$id" )
    verify_obj_in_ns package $pkg "ns2-$id" || (log "package $pkg not found in ns2-$id" &&
    cleanup python "ns1-$id" func1 "ns2-$id" $pkg "ns2-$id" && exit 1)
    verify_obj_in_ns httptrigger $ht "ns2-$id" || (log "http trigger $ht not found in ns2-$id" &&
    cleanup python "ns1-$id" func1 "ns2-$id" $pkg "ns2-$id" $ht "ns2-$id" && exit 1)

    sleep 3
    # get the function loaded into a pod
    response=$(curl http://$FISSION_ROUTER/func1)
    echo $response | grep "Hello" || (log "response Hello not received" &&
    cleanup python "ns1-$id" func1 "ns2-$id" $pkg "ns2-$id" $ht "ns2-$id" && exit 1)

    # note that the env pod is created in the ns that env is created
    # so even if the function is created in a different ns, they will be loaded in the env pods ns.
    # this behavior is for poolmgr so that functions can utilize the resources better.
    verify_function_pod_ns func1 "ns1-$id" || (log "func func1 not specialized in ns1-$id" &&
    cleanup python "ns1-$id" func1 "ns2-$id" $pkg "ns2-$id" $ht "ns2-$id" && exit 1)

    cleanup python "ns1-$id" func1 "ns2-$id" $pkg "ns2-$id" $ht "ns2-$id"
}


main() {
    # extract the test-id generated for this CI test run, so that they can be suffixed to namespaces created as part of
    # this test and namespaces wont clash when fission CI tests are run in parallel in the future.
    id=`echo $FISSION_NAMESPACE| cut -d"-" -f2`

    echo "test_id : $id"

    # create source code
    create_python_source_code

    # pool mgr tests
    # 1. env ns1, func ns2 with code, route and verify specialized pod in ns1, also verify pkg in ns2
    pool_mgr_test_1

    # 3. env default, func with code in ns2, route and verify specialized pod in $FUNCTION_NAMESPACE.
    # this test is to verify backward compatibility
    pool_mgr_test_2

    # builder mgr tests
    # 1. env with builder image and runtime image in ns1, src pkg and func in ns2, route and verify specialized pod in ns1
    builder_mgr_test_1

    # 2. env with builder image and runtime image in default, src pkg and func in ns2,
    #    route and verify specialized pod in fission-builder. this test is to verify backward compatibility
    builder_mgr_test_2

    # new deploy mgr tests
    # 1. env ns1, func ns2 with code, route and verify specialized pod in ns2,
    new_deploy_mgr_and_internal_route_test_1

    # 2. env default, func ns2 with code, route and verify specialized pod in fission-function
    new_deploy_mgr_and_internal_route_test_2

    # internal route tests. ( combined this with new deploy mgr tests, so we dont have to recreate envs and funcs again )
    # 1. env ns1, func ns2 with code, curl http://FISSION_ROUTER/fission-function/ns2/func -> should work
    # 2. env in default, func ns2 with code, curl http://FISSION_ROUTER/fission-function/func -> should work


    # TODO : add following tests
    # timer trigger tests.
    # 1. env ns1, func ns2 with code, tt ( with a one time cron string for executing imm'ly), verify function is executed
    # this also indirectly tests internal route established at http://FISSION_ROUTER/ns1/func

    # 2. env in default, func ns2 with code, tt ( with a one time cron string for executing imm'ly), verify function is executed
    # this also indirectly tests internal route established at http://FISSION_ROUTER/func


    # kube watch tests.
    # 1. env ns1, func ns2 with code, watch Trigger ( TBD), verify function is executed
    # this also indirectly tests internal route established at http://FISSION_ROUTER/ns1/func

    # 2. env in default, func ns2 with code, watch Trigger ( TBD ), verify function is executed
    # this also indirectly tests internal route established at http://FISSION_ROUTER/func


    # msq trigger tests.
    # integrate after mqtrigger tests are checked into master.

    final_cleanup
}

main
