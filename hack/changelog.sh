#!/bin/bash

set -e
#set -x

DIR=$(realpath $(dirname "$0"))/../
export GITHUB_TOKEN=$(cat ~/.github-token)
github_changelog_generator -u fission -p fission -t "${GITHUB_TOKEN}" --no-issues -o tmp_CHANGELOG.md
mv tmp_CHANGELOG.md "${DIR}"/CHANGELOG.md
