#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

ROOT=`realpath $(dirname $0)/..`

# Currently here only lists the packages that already addressed all warnings for gradual code repair.
# That means we don't allow adding new warnings to any of package in list. And eventually, the
# list will be replaced by find command.
declare -a pkgs=("pkg/builder" "pkg/builder" "pkg/crd" "pkg/logger" "pkg/buildermgr" "pkg/fission-cli" "cmd/fission-cli")

for pkg in "${pkgs[@]}"
do
    find ${ROOT}/${pkg} -type d |grep -v influxdb | xargs -I@ staticcheck @
done
