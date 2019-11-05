#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

ROOT=`realpath $(dirname $0)/..`

go list ./...| grep -v vendor | grep -v "examples" | grep -v "demos" | xargs -I@ staticcheck @
