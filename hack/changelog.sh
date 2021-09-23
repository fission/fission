#!/bin/bash

set -e
#set -x

DIR=$(realpath $(dirname "$0"))/../
export GITHUB_TOKEN=$(cat ~/.github-token)
# ignore_tags lists tags which we dont want to include in the changelog
# and non standard releases
exclude_tags=$(tr '\n' ',' < hack/ignore_tags | sed 's/,$/\n/')
github_changelog_generator -u fission -p fission -t "${GITHUB_TOKEN}" \
    --no-issues --exclude-labels "no-changelog" -o tmp_CHANGELOG.md \
    --exclude-tags $exclude_tags
mv tmp_CHANGELOG.md "${DIR}"/CHANGELOG.md
