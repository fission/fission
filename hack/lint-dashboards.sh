#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

gobin="$(go env GOBIN)"
gobin="${gobin:-$(go env GOPATH | cut -d: -f1)/bin}"
export PATH="$PATH:$gobin"

if ! command -v dashboard-linter >/dev/null 2>&1; then
    echo "dashboard-linter is not installed"
    echo "Installing dashboard-linter..."
    # dashboard-linter v0.1.1, pinned by commit (OSSF Scorecard PinnedDependencies).
    # `go install pkg@version` is rejected because this version's go.mod has
    # replace directives, so clone at the pinned commit and build instead.
    linter_sha=1be3836b83fbcf9508efcd87af87dfbfbec94279
    linter_src=$(mktemp -d)
    trap 'rm -rf "$linter_src"' EXIT
    git clone --quiet https://github.com/grafana/dashboard-linter "$linter_src" || exit 1
    git -C "$linter_src" -c advice.detachedHead=false checkout --quiet "$linter_sha" || exit 1
    mkdir -p "$gobin"
    (cd "$linter_src" && go build -o "$gobin/dashboard-linter" .) || exit 1
fi
BASE_PATH=$(pwd)

if [[ -z "$BASE_PATH" ]] ; then
    BASE_PATH=$(GITHUB_WORKSPACE)
fi

DASHBOARD_PATH="$BASE_PATH/charts/fission-all/dashboards/*"

for f in $DASHBOARD_PATH
do
    dashboard-linter lint --strict --verbose $f
done