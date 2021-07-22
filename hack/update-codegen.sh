#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

if [ ! -d "../code-generator" ]; then
	echo "Please get code-generator from fission org"
	exit 1
fi

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; ls -d -1 ./vendor/k8s.io/code-generator 2>/dev/null || echo ../code-generator)}

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client,informer,lister" \
	github.com/fission/fission/pkg/generated \
	github.com/fission/fission/pkg/apis \
	"core:v1" \
	--output-base "$(dirname "${BASH_SOURCE[0]}")/../../../.." \
	--go-header-file "$(dirname "${BASH_SOURCE[0]}")/boilerplate.txt"
