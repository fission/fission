#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

go vet -v $(go list ./...| grep -v vendor)
