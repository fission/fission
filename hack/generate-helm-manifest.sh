#!/bin/bash

set -e
set -x

DIR=$(realpath $(dirname "$0"))/../
MANIFESTDIR=$(realpath "$DIR")/manifest
CHARTS="fission-all"

source $(realpath "${DIR}"/test/init_tools.sh)
doit() {
    echo "! $*"
    "$@"
}

check_charts_repo() {
    local chartsrepo=$1

    if [ ! -d "$chartsrepo" ]; then
        echo "Error finding chart repo at $chartsrepo"
        exit 1
    fi
    echo "check_charts_repo == PASSED"
}

update_chart_version() {
    pushd "$DIR"/charts
    local version=$1
    for c in $CHARTS; do
        sed -i "s/^version.*/version\: ${version}/" $c/Chart.yaml
        sed -i "s/appVersion.*/appVersion\: ${version}/" $c/Chart.yaml
        sed -i "s/\bimageTag:.*/imageTag\: ${version}/" $c/values.yaml
    done
    popd
}

lint_charts() {
    pushd "$DIR"/charts
    for c in $CHARTS; do
        doit helm lint $c
        if [ $? -ne 0 ]; then
            echo "helm lint failed"
            exit 1
        fi
    done
    popd
}

build_charts() {
    mkdir -p "$MANIFESTDIR"/charts
    pushd "$DIR"/charts
    find . -iname *.~?~ | xargs -r rm
    for c in $CHARTS; do
        doit helm package -u $c/
        mv ./*.tgz "$MANIFESTDIR"/charts/
    done
    popd
}

build_yamls() {
    local version=$1

    mkdir -p "${MANIFESTDIR}"/yamls
    pushd "${DIR}"/charts
    find . -iname *.~?~ | xargs -r rm

    releaseName=fission-$(echo "${version}" | sed 's/\./-/g')

    for c in $CHARTS; do
        # fetch dependencies
        pushd ${c}
        doit helm dependency update
        popd

        echo "Release name", "$releaseName"
        cmdprefix="helm template ${releaseName} ${c} --namespace fission --validate"

        # for minikube and other environments that don't support LoadBalancer
        command="$cmdprefix --set analytics=false,analyticsNonHelmInstall=true,serviceType=NodePort,routerServiceType=NodePort"
        echo "$command"
        $command >${c}-"${version}"-minikube.yaml

        # for environments that support LoadBalancer
        command="$cmdprefix --set analytics=false,analyticsNonHelmInstall=true"
        echo "$command"
        $command >${c}-"${version}".yaml

        # for OpenShift
        command="$cmdprefix --set analytics=false,analyticsNonHelmInstall=true,logger.enableSecurityContext=true"
        echo "$command"
        $command >${c}-"${version}"-openshift.yaml

        # copy yaml files to build directory
        mv ./*.yaml "${MANIFESTDIR}"/yamls/
    done

    popd
}

update_github_charts_repo() {
    local version=$1
    local chartsrepo=$2

    pushd "$chartsrepo"
    for c in $CHARTS; do
        cp "$MANIFESTDIR"/charts/$c-"${version}".tgz .
        ./index.sh
    done
    popd
}

version=$1
if [ -z "$version" ]; then
    echo "Release version not mentioned"
    exit 1
fi

echo "Current version for release: $version"

chartsrepo="$DIR../fission-charts"
check_charts_repo "$chartsrepo"

# Build manifests and charts
lint_charts
update_chart_version "$version"
lint_charts
build_yamls "$version"
build_charts
update_github_charts_repo "$version" "$chartsrepo"
