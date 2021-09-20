#!/bin/bash

set -e
set -x

DIR=$(realpath $(dirname "$0"))/../
MANIFESTDIR=$(realpath "$DIR")/manifest

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
    local version=$1
    sed -i "s/^version.*/version\: ${version}/" charts/fission-core/Chart.yaml
    sed -i "s/^version.*/version\: ${version}/" charts/fission-all/Chart.yaml
    sed -i "s/appVersion.*/appVersion\: ${version}/" charts/fission-core/Chart.yaml
    sed -i "s/appVersion.*/appVersion\: ${version}/" charts/fission-all/Chart.yaml
    sed -i "s/\bimageTag:.*/imageTag\: ${version}/" charts/fission-core/values.yaml
    sed -i "s/\bimageTag:.*/imageTag\: ${version}/" charts/fission-all/values.yaml
}

lint_charts() {
    helm lint charts/fission-all charts/fission-core
    if [ $? -ne 0 ]; then
        echo "helm lint failed"
        exit 1
    fi
}

build_charts() {
    mkdir -p "$MANIFESTDIR"/charts
    pushd "$DIR"/charts
    find . -iname *.~?~ | xargs -r rm
    for c in fission-all fission-core; do
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

    for c in fission-all fission-core; do
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
        command="$cmdprefix --set analytics=false,analyticsNonHelmInstall=true,logger.enableSecurityContext=true,prometheus.enabled=false"
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
    cp "$MANIFESTDIR"/charts/fission-all-"${version}".tgz .
    cp "$MANIFESTDIR"/charts/fission-core-"${version}".tgz .
    ./index.sh
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
