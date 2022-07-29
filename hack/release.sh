#!/bin/bash

set -e
#set -x

DIR=$(realpath $(dirname "$0"))/../
MANIFESTDIR=$(realpath "$DIR")/manifest

doit() {
    echo "! $*"
    "$@"
}

check_commands() {
    if ! command -v goreleaser >/dev/null; then
        echo "Goreleaser CLI not found. Please get from https://goreleaser.com/install/"
        exit 1
    fi
    echo "check_commands == PASSED"
}

# Ensure we're on the master branch
check_branch() {
    curr_branch=$(git rev-parse --abbrev-ref HEAD)
    if [ "$curr_branch" != "master" ]; then
        echo "Not on master branch."
        exit 1
    fi
    echo "check_branch == PASSED"
}

# Ensure working dir is clean
check_clean() {
    if ! git diff-index --quiet HEAD --; then
        echo "Unclean tree"
        exit 1
    fi
    echo "check_clean == PASSED"
}

check_github_token() {
    if [ ! -f "$HOME"/.github-token ] && [ -z "$GITHUB_TOKEN" ]; then
        echo "Error finding github access token at ${HOME}/.github-token or in GITHUB_TOKEN envvar"
        exit 1
    fi
    echo "check_github_token == PASSED"
}

# Read token from file if not set as envvar
if [ -z "$GITHUB_TOKEN"]; then
    export GITHUB_TOKEN=$(cat ~/.github-token)
fi

version=$1
if [ -z "$version" ]; then
    echo "Release version not mentioned"
    exit 1
fi

echo "Current version for release: $version"

chartsrepo="$DIR../fission-charts"

# Prechecks
check_clean
check_commands
check_branch
check_github_token

export GORELEASER_CURRENT_TAG=$version
echo "Release version $GORELEASER_CURRENT_TAG "
echo "DOCKER_CLI_EXPERIMENTAL $DOCKER_CLI_EXPERIMENTAL"
goreleaser release

echo "############ DONE #############"
echo "Congratulation, ${version} is ready to ship !!"
echo "Run ./hack/release-tag.sh and publish release."
echo "Don't forget to push chart repo changes & update CHANGELOG.md"
echo "##############################"
