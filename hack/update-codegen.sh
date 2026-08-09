#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)

# Upstream client-gen cannot generate a client for the Package CRD: gengo's
# "private" name system lowercases the type to "package", a Go keyword.
#
# Rather than fork k8s.io/code-generator, copy the module out of the read-only
# module cache and swap in our own client-gen entrypoint, which wraps the
# namer. kube_codegen.sh sets KUBE_CODEGEN_ROOT to its own directory and runs
# `go install` from there, so the copy is what gets built -- no `replace`
# directive is involved. See hack/codegen/clientgen/main.go.
UPSTREAM_PKG=$(go list -m -f '{{.Dir}}' k8s.io/code-generator)
if [ -z "${UPSTREAM_PKG}" ] || [ ! -d "${UPSTREAM_PKG}" ]; then
	echo "Error: could not locate the k8s.io/code-generator module directory"
	echo "Received output: '${UPSTREAM_PKG}'"
	exit 1
fi

CODEGEN_PKG=$(mktemp -d)
# Module cache files are read-only; make the copy writable so it can be patched
# and so the trap can remove it.
trap 'chmod -R u+w "${CODEGEN_PKG}" 2>/dev/null || true; rm -rf "${CODEGEN_PKG}"' EXIT

cp -R "${UPSTREAM_PKG}/." "${CODEGEN_PKG}/"
chmod -R u+w "${CODEGEN_PKG}"
cp "${SCRIPT_ROOT}/hack/codegen/clientgen/main.go" "${CODEGEN_PKG}/cmd/client-gen/main.go"

echo "Generating code under ${SCRIPT_ROOT}/pkg/generated using ${UPSTREAM_PKG} ..."

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
