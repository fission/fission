#!/bin/bash

set -e
#set -x

DIR=$(realpath $(dirname $0))/../
BUILDDIR=$(realpath $DIR)/build

artifacts=()
source $(realpath ${DIR}/test/init_tools.sh)

# Ensure we're on the master branch
check_branch() {
    local version=$1
    curr_branch=$(git rev-parse --abbrev-ref HEAD)
    if [ $curr_branch != "release-${version}" ]; then
        echo "Not on release-${version} branch."
        exit 1
    fi
}

# Ensure working dir is clean
check_clean() {
    if ! git diff-index --quiet HEAD --; then
        echo "Unclean tree"
        exit 1
    fi
}

attach_github_release_cli() {
    # cli
    echo "Artifact for osx amd64 cli"
    artifacts+=("$BUILDDIR/darwin/amd64/$version/fission#fission-cli-osx")
    echo "Artifact for windows amd64 cli"
    artifacts+=("$BUILDDIR/windows/amd64/$version/fission.exe#fission-cli-windows.exe")
    echo "Artifact for linux amd64 cli"
    artifacts+=("$BUILDDIR/linux/amd64/$version/fission#fission-cli-linux")
    echo "Artifact for linux arm cli"
    artifacts+=("$BUILDDIR/linux/arm/$version/fission#fission-cli-linux-arm")
    echo "Artifact for linux arm64 cli"
    artifacts+=("$BUILDDIR/linux/arm64/$version/fission#fission-cli-linux-arm64")
}

attach_github_release_charts() {
    local version=$1
    echo "fission-all chart"
    artifacts+=("$BUILDDIR/charts/fission-all-$version.tgz#fission-all-$version.tgz")
    echo "Fission-core chart"
    artifacts+=("$BUILDDIR/charts/fission-core-$version.tgz#fission-core-$version.tgz")
}

attach_github_release_yamls() {
    local version=$1

    for c in fission-all fission-core; do
        # YAML
        artifacts+=("$BUILDDIR/yamls/${c}-${version}-minikube.yaml#${c}-${version}-minikube.yaml")
        artifacts+=("$BUILDDIR/yamls/${c}-${version}.yaml#${c}-${version}.yaml")
        artifacts+=("$BUILDDIR/yamls/${c}-${version}-openshift.yaml#${c}-${version}-openshift.yaml")
    done
}

update_github_charts_repo() {
    local version=$1
    local chartsrepo=$2

    pushd $chartsrepo
    cp $BUILDDIR/charts/fission-all-${version}.tgz .
    cp $BUILDDIR/charts/fission-core-${version}.tgz .
    ./index.sh
    popd
}

tag_and_release() {
    local version=$1
    local gittag=$version
    local prefix="v"
    local gopkgtag=${version/#/${prefix}}

    if [[ ${version} == v* ]]; then # if version starts with "v", don't append prefix.
        gopkgtag=${version}
    fi

    # tag the release
    git tag $gittag
    git tag -a $gopkgtag -m "Fission $gopkgtag"

    # push tag
    git push origin $gittag
    git push origin $gopkgtag

    relnotes="
Install Guide: https://docs.fission.io/installation/
Full Changelog: https://github.com/fission/fission/blob/master/CHANGELOG.md    
"
    gh release create $gittag --prerelease  --title $gittag --notes $relnotes --target $gitcommit ${artifacts[@]}
}

generate_changelog() {
    local version=$1

    echo "# ${version}" >new_CHANGELOG.md
    echo
    echo "[Documentation](https://docs.fission.io/)" >>new_CHANGELOG.md
    echo

    create_downloads_table ${version} >>new_CHANGELOG.md

    # generate changelog from github
    github_changelog_generator -u fission -p fission -t ${GITHUB_TOKEN} --future-release ${version} --no-issues -o tmp_CHANGELOG.md
    sed -i '$ d' tmp_CHANGELOG.md

    # concatenate two files
    cat tmp_CHANGELOG.md >>new_CHANGELOG.md
    mv new_CHANGELOG.md ${DIR}/CHANGELOG.md

    rm tmp_CHANGELOG.md
}

create_downloads_table() {
    local release_tag=$1
    local url_prefix="https://github.com/fission/fission/releases/download"

    echo "## Downloads for ${version}"
    echo

    local files=$(find build -name '*' -type f)

    echo
    echo "filename | sha256 hash"
    echo "-------- | -----------"
    for file in ${artifacts[@]}; do
        filepath=$(echo $file | cut -d'#' -f 1)
        filename=$(echo $file | cut -d'#' -f 2)
        echo "[${filename}]($url_prefix/$release_tag/${filename}) | \`$(shasum -a 256 ${filepath} | cut -d' ' -f 1)\`"
    done
    echo
}
export -f create_downloads_table

release_environment_check() {
    local version=$1
    local chartsrepo=$2

    check_branch $version
    check_clean

    if [ ! -f $HOME/.github-token ]; then
        echo "Error finding github access token at ${HOME}/.github-token"
        exit 1
    fi

    if [ ! -d $chartsrepo ]; then
        echo "Error finding chart repo at $chartsrepo"
        exit 1
    fi

    if [ ! -d $FISSION_HOME ]; then
        echo "The FISSION_HOME variable should be set to directory where Fission and fission-charts are checked out"
        exit 1
    fi
}

build_charts() {
    local version=$1
    mkdir -p $BUILDDIR/charts
    pushd $DIR/charts
    find . -iname *.~?~ | xargs -r rm
    for c in fission-all fission-core; do
        helm package -u $c/
        mv *.tgz $BUILDDIR/charts/
    done
    popd
}

build_yamls() {
    local version=$1

    mkdir -p ${BUILDDIR}/yamls
    pushd ${DIR}/charts
    find . -iname *.~?~ | xargs -r rm

    releaseName=fission-$(echo ${version} | sed 's/\./-/g')

    for c in fission-all fission-core; do
        # fetch dependencies
        pushd ${c}
        helm dependency update
        popd

        # for minikube and other environments that don't support LoadBalancer
        helm template ${c} -n ${releaseName} --namespace fission --set analytics=false,analyticsNonHelmInstall=true,serviceType=NodePort,routerServiceType=NodePort >${c}-${version}-minikube.yaml
        # for environments that support LoadBalancer
        helm template ${c} -n ${releaseName} --namespace fission --set analytics=false,analyticsNonHelmInstall=true >${c}-${version}.yaml
        # for OpenShift
        helm template ${c} -n ${releaseName} --namespace fission --set analytics=false,analyticsNonHelmInstall=true,logger.enableSecurityContext=true,prometheus.enabled=false >${c}-${version}-openshift.yaml

        # copy yaml files to build directory
        mv *.yaml ${BUILDDIR}/yamls/
    done

    popd
}

build_all() {
    local version=$1

    if [ -z "$version" ]; then
        echo "Version unspecified"
        exit 1
    fi

    local date=$2

    if [ -z "$date" ]; then
        echo "Build date unspecified"
        exit 1
    fi

    local gitcommit=$3

    if [ -z "gitcommit" ]; then
        echo "Git commit unspecified"
        exit 1
    fi

    if [ -e $BUILDDIR ]; then
        echo "Removing existing build dir ($BUILDDIR)."
        rm -rf $BUILDDIR
    fi

    mkdir -p $BUILDDIR

    # generate swagger (OpenApi 2.0) doc before building bundle image
    VERSION=$version TIMESTAMP=$date COMMITSHA=$gitcommit make generate-swagger-doc

    # Build CLI for all platforms
    VERSION=$version TIMESTAMP=$date COMMITSHA=$gitcommit make all-fission-cli
}

build_images() {
    local version=$1
    if [ -z "$version" ]; then
        echo "Version unspecified"
        exit 1
    fi

    local date=$2
    if [ -z "$date" ]; then
        echo "Build date unspecified"
        exit 1
    fi

    local gitcommit=$3
    if [ -z "gitcommit" ]; then
        echo "Git commit unspecified"
        exit 1
    fi

    # Build and push all images
    VERSION=$version TAG=$version TIMESTAMP=$date COMMITSHA=$gitcommit make all-images
    VERSION=$version TAG=latest TIMESTAMP=$date COMMITSHA=$gitcommit make all-images
}

check_commands() {
    if ! command -v hub >/dev/null; then
        echo "Github CLI hub not found. Please get from https://cli.github.com/"
    fi
}

export GITHUB_TOKEN=$(cat ~/.github-token)

version=$1
if [ -z $version ]; then
    echo "Release version not mentioned"
    exit 1
fi

date=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
gitcommit=$(git rev-parse HEAD)

chartsrepo=$2
if [ -z $chartsrepo ]; then
    chartsrepo="$DIR../fission-charts"
fi

check_commands
release_environment_check $version $chartsrepo
build_all $version $date $gitcommit
# build_images $version $date $gitcommit
build_charts $version
build_yamls $version

attach_github_release_cli $version
attach_github_release_charts $version
attach_github_release_yamls $version
update_github_charts_repo $version $chartsrepo
generate_changelog $version
tag_and_release $version

echo "############ DONE #############"
echo "Congratulation, ${version} is ready to ship !!"
echo "Don't forget to push chart repo changes & update CHANGELOG.md"
echo "##############################"
