#!/bin/bash

if [ ! -z "${KUBECONFIG}" ]
then
    if [ ! -f "${KUBECONFIG}" ]
    then
        unset KUBECONFIG
    else
        K="kubectl --kubeconfig $KUBECONFIG --namespace default"
        if $K get configmap ok-to-destroy
        then
        set +e  # do not break if crd not found
        $K delete functions --all
        $K delete environments --all
        $K delete httptriggers --all
        $K delete kuberneteswatchtriggers --all
        $K delete messagequeuetriggers --all
        $K delete packages --all
        $K delete timetriggers --all
        set -e
        fi
    fi
fi

command -v fission && fission support dump