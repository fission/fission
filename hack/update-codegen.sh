#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

CODEGEN_PKG_VERSION=$(go list -m -f '{{.Replace.Version}}' k8s.io/code-generator)
if [ -z "$CODEGEN_PKG_VERSION" ]; then
	echo "Error: could not determine code-generator version from go.mod"
	echo "Received output: '$CODEGEN_PKG_VERSION'"
	exit 1
fi
GOPATH=$(go env GOPATH)
GOMODCACHEPATH=$(go env GOMODCACHE)
SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-$(
	cd "${SCRIPT_ROOT}"
	echo ${GOMODCACHEPATH}/github.com/fission/code-generator@${CODEGEN_PKG_VERSION}
)}

echo "Generating code under ${SCRIPT_ROOT}/pkg/generated using ${CODEGEN_PKG} ..."

source "${CODEGEN_PKG}/kube_codegen.sh"

THIS_PKG="github.com/fission/fission"

kube::codegen::gen_register \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}"

kube::codegen::gen_client \
	--with-watch \
	--with-applyconfig \
	--output-pkg ${THIS_PKG}/pkg/generated \
	--output-dir "${SCRIPT_ROOT}/pkg/generated" \
	--boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
	"${SCRIPT_ROOT}/pkg/apis"
