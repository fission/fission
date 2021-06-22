#!/bin/bash

set -e
#set -x

DIR=$(realpath $(dirname "$0"))/../
BUILDDIR=$(realpath "$DIR")/build

artifacts=()
source $(realpath "${DIR}"/test/init_tools.sh)

doit() {
    echo "! $*"
    "$@"
}

# Ensure we're on the master branch
check_branch() {
    local version=$1
    curr_branch=$(git rev-parse --abbrev-ref HEAD)
    if [ "$curr_branch" != "release-${version}" ]; then
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
    artifacts+=("$BUILDDIR/bin/fission-$version-darwin-amd64")
    echo "Artifact for windows amd64 cli"
    artifacts+=("$BUILDDIR/bin/fission-$version-windows-amd64.exe")
    echo "Artifact for linux amd64 cli"
    artifacts+=("$BUILDDIR/bin/fission-$version-linux-amd64")
    echo "Artifact for linux arm cli"
    artifacts+=("$BUILDDIR/bin/fission-$version-linux-arm")
    echo "Artifact for linux arm64 cli"
    artifacts+=("$BUILDDIR/bin/fission-$version-linux-arm64")
}

attach_github_release_charts() {
    local version=$1
    echo "fission-all chart"
    artifacts+=("$BUILDDIR/charts/fission-all-$version.tgz")
    echo "Fission-core chart"
    artifacts+=("$BUILDDIR/charts/fission-core-$version.tgz")
}

attach_github_release_yamls() {
    local version=$1

    for c in fission-all fission-core; do
        # YAML
        artifacts+=("$BUILDDIR/yamls/${c}-${version}-minikube.yaml")
        artifacts+=("$BUILDDIR/yamls/${c}-${version}.yaml")
        artifacts+=("$BUILDDIR/yamls/${c}-${version}-openshift.yaml")
    done
}

update_github_charts_repo() {
    local version=$1
    local chartsrepo=$2

    pushd "$chartsrepo"
    cp "$BUILDDIR"/charts/fission-all-"${version}".tgz .
    cp "$BUILDDIR"/charts/fission-core-"${version}".tgz .
    ./index.sh
    popd
}

gh_release() {
    local version=$1
    cp "${DIR}"/hack/notes.md relnotes.md
    create_downloads_table ${version} >> relnotes.md
    doit gh release create "$version" --draft --prerelease --title "$version" --notes-file $(realpath "${DIR}"/relnotes.md) --target "$gitcommit" "${artifacts[@]}"
}

generate_changelog() {
    local version=$1

    echo "# ${version}" >new_CHANGELOG.md
    echo
    echo "[Documentation](https://docs.fission.io/)" >>new_CHANGELOG.md
    echo

    # generate changelog from github
    github_changelog_generator -u fission -p fission -t "${GITHUB_TOKEN}" --future-release "${version}" --no-issues -o tmp_CHANGELOG.md
    sed -i '$ d' tmp_CHANGELOG.md

    # concatenate two files
    cat tmp_CHANGELOG.md >>new_CHANGELOG.md
    mv new_CHANGELOG.md "${DIR}"/CHANGELOG.md

    rm tmp_CHANGELOG.md
}

create_downloads_table() {
    local release_tag=$1
    local url_prefix="https://github.com/fission/fission/releases/download"

    echo
    echo
    echo "#### Downloads for ${version}"
    echo

    echo
    echo "filename | sha256 hash"
    echo "-------- | -----------"
    for file in ${artifacts[@]}; do
        filename=${file##*/}
        echo "[${filename}]($url_prefix/$release_tag/${filename}) | \`$(shasum -a 256 ${file} | cut -d' ' -f 1)\`"
    done
    echo
}
export -f create_downloads_table

release_environment_check() {
    local version=$1
    local chartsrepo=$2

    check_branch "$version"
    check_clean

    if [ ! -f "$HOME"/.github-token ]; then
        echo "Error finding github access token at ${HOME}/.github-token"
        exit 1
    fi

    if [ ! -d "$chartsrepo" ]; then
        echo "Error finding chart repo at $chartsrepo"
        exit 1
    fi
}

build_charts() {
    local version=$1
    mkdir -p "$BUILDDIR"/charts
    pushd "$DIR"/charts
    find . -iname *.~?~ | xargs -r rm
    for c in fission-all fission-core; do
        doit helm package -u $c/
        mv ./*.tgz "$BUILDDIR"/charts/
    done
    popd
}

build_yamls() {
    local version=$1

    mkdir -p "${BUILDDIR}"/yamls
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
        mv ./*.yaml "${BUILDDIR}"/yamls/
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

    if [ -z "$gitcommit" ]; then
        echo "Git commit unspecified"
        exit 1
    fi

    if [ -e "$BUILDDIR" ]; then
        echo "Removing existing build dir ($BUILDDIR)."
        rm -rf "$BUILDDIR"
    fi

    mkdir -p "$BUILDDIR"

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
    if [ -z "$gitcommit" ]; then
        echo "Git commit unspecified"
        exit 1
    fi

    # Build and push all images
    REPO=fission VERSION=$version TAG=latest TIMESTAMP=$date COMMITSHA=$gitcommit make all-images
    REPO=fission VERSION=$version TAG=$version TIMESTAMP=$date COMMITSHA=$gitcommit make all-images
}

check_commands() {
    if ! command -v hub >/dev/null; then
        echo "Github CLI hub not found. Please get from https://cli.github.com/"
    fi
}

export GITHUB_TOKEN=$(cat ~/.github-token)

version=$1
if [ -z "$version" ]; then
    echo "Release version not mentioned"
    exit 1
fi

date=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
gitcommit=$(git rev-parse HEAD)

chartsrepo=$2
if [ -z "$chartsrepo" ]; then
    chartsrepo="$DIR../fission-charts"
fi

check_commands
release_environment_check "$version" "$chartsrepo"
build_all "$version" "$date" "$gitcommit"
build_charts "$version"
build_yamls "$version"

attach_github_release_cli "$version"
attach_github_release_charts "$version"
attach_github_release_yamls "$version"
update_github_charts_repo "$version" "$chartsrepo"
gh_release "$version"

generate_changelog "$version"

build_images "$version" "$date" "$gitcommit"

echo "############ DONE #############"
echo "Congratulation, ${version} is ready to ship !!"
echo "Run ./hack/release-tag.sh and publish release."
echo "Don't forget to push chart repo changes & update CHANGELOG.md"
echo "##############################"
