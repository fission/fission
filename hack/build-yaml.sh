#!/bin/bash

set -e
set -x

DIR=${GITHUB_WORKSPACE}
MANIFESTDIR=${GITHUB_WORKSPACE}/manifest
CHARTPATH=${GITHUB_WORKSPACE}/charts/fission-all

source $(realpath "${DIR}"/test/init_tools.sh)
doit() {
    echo "! $*"
    "$@"
}

build_yamls() {
    local version=$1

    mkdir -p "${MANIFESTDIR}"/yamls
    releaseName=fission-$(echo "${version}" | sed 's/\./-/g')

    cd $CHARTPATH
    doit helm dependency update
    cd $GITHUB_WORKSPACE

    echo "Release name", "$releaseName"
    cmdprefix="helm template ${releaseName} ${CHARTPATH} --namespace fission --validate"

    # for minikube and other environments that don't support LoadBalancer
    command="$cmdprefix --set analytics=false,analyticsNonHelmInstall=true,serviceType=NodePort,routerServiceType=NodePort"
    echo "$command"
    $command >"$MANIFESTDIR/yamls/fission-all-${version}"-minikube.yaml

    # for environments that support LoadBalancer
    command="$cmdprefix --set analytics=false,analyticsNonHelmInstall=true"
    echo "$command"
    $command >"$MANIFESTDIR/yamls/fission-all-${version}".yaml

    # for OpenShift
    command="$cmdprefix --set analytics=false,analyticsNonHelmInstall=true,logger.enableSecurityContext=true"
    echo "$command"
    $command >"$MANIFESTDIR/yamls/fission-all-${version}"-openshift.yaml

}


version=$1
if [ -z "$version" ]; then
    echo "Release version not mentioned"
    exit 1
fi

echo "Current version for release: $version"

build_yamls "$version"