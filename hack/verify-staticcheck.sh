#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

go list ./...| grep -v vendor | grep -v "examples" | grep -v "demos" | grep -v "test" | xargs -I@ staticcheck @
