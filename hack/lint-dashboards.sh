#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

if ! command -v dashboard-linter >/dev/null 2>&1; then
    echo "dashboard-linter is not installed"
    echo "Installing dashboard-linter..."
    go install github.com/grafana/dashboard-linter@1be3836b83fbcf9508efcd87af87dfbfbec94279 # v0.1.1 || exit 1
    gobin="$(go env GOBIN)"
    export PATH="$PATH:${gobin:-$(go env GOPATH | cut -d: -f1)/bin}"
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