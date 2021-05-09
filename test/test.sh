#!/bin/bash

set -euo pipefail

# Unbound variables cause failure, so this readable if block instead of Parameter Expansion
if [[ ${TRAVIS_EVENT_TYPE+NOVALUE} == "cronNOVALUE" ]]
then
    echo "Skipping build & test, this is cron job for fission upgrade tests"
    exit 0
fi

if [ ! -f ${HOME}/.kube/config ]
then
    echo "Skipping end to end tests, no cluster credentials"
    exit 0
fi

source $(dirname $0)/test_utils.sh

dump_system_info

setupCIBuildEnv

while true; do
    # ensure that gke cluster is now free for testing

    previous_build_id=$(kubectl --namespace default get configmap in-test --ignore-not-found -o=jsonpath='{.metadata.labels.travisID}')

    if [[ ! -z ${previous_build_id} ]]; then
        build_state=$(curl -s -X GET https://api.travis-ci.org/build/${previous_build_id} \
        -H "Authorization: token ${TRAVIS_TOKEN}" \
        -H "Travis-API-Version: 3" | python -c "import sys,json; print json.load(sys.stdin)['state']")

        # If previous build state is not equal to "started" or the previous build id
        # equals to the current build ID means the previous build is end or restart.
        # We can remove the configmap safely and start next k8s test safely.
        if [[ ${TRAVIS_BUILD_ID} == ${previous_build_id} ]] || [[ $build_state != "started" ]]; then
            kubectl --namespace default delete configmap -l travisID=${previous_build_id}
        fi
    fi

    created=$(kubectl --namespace default create configmap in-test|grep "created"||true)
    if [[ -z $created ]]; then
        echo "Cluster is now in used. Retrying after 15 seconds..."
        sleep 15
        continue
    fi
    kubectl --namespace default label configmap in-test travisID=${TRAVIS_BUILD_ID}
    break
done

install_and_test $REPO $IMAGE $TAG $FETCHER_IMAGE $TAG $PRUNE_INTERVAL $ROUTER_SERVICE_TYPE $SERVICE_TYPE $PRE_UPGRADE_CHECK_IMAGE $REPORTER_IMAGE
