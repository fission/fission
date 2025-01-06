#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

GOPATH=$(go env GOPATH)
SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-$(
	cd "${SCRIPT_ROOT}"
	ls -d -1 ${SCRIPT_ROOT}/../code-generator 2>/dev/null || echo $GOPATH/pkg/mod/k8s.io/code-generator@v0.32.0
)}
OUTDIR="${SCRIPT_ROOT}/pkg/generated"

echo "Generating code under ${OUTDIR} using ${CODEGEN_PKG} ..."

source "${CODEGEN_PKG}/kube_codegen.sh"

# kube::codegen::gen_helpers \
#     --input-pkg-root github.com/fission/fission/pkg/apis \
#     --output-base "${OUTDIR}" \
#     --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.txt"

kube::codegen::gen_client \
	--with-watch \
	--with-applyconfig \
	--output-pkg github.com/fission/fission/pkg/generated \
	--output-dir "${OUTDIR}" \
	--boilerplate "${SCRIPT_ROOT}/hack/boilerplate.txt" \
	"${SCRIPT_ROOT}/pkg/apis" \
