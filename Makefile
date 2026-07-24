# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

.DEFAULT_GOAL := check

SKAFFOLD_PROFILE ?= kind

VERSION ?= v0.0.0
TIMESTAMP ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
COMMITSHA ?= $(shell git rev-parse HEAD)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GOAMD64 ?= $(shell go env GOAMD64)
GOPATH ?= $(shell go env GOPATH)

FISSION-CLI-SUFFIX :=
ifeq ($(GOOS), windows)
	FISSION-CLI-SUFFIX := .exe
endif

# Show this help.
help:
	@awk '/^#/{c=substr($$0,3);next}c&&/^[[:alpha:]][[:alnum:]_-]+:/{print substr($$1,1,index($$1,":")),c}1{c=0}' $(MAKEFILE_LIST) | column -s: -t

print-%:
	@echo '$*=$($*)'

debug-vars: print-GOOS print-GOARCH print-GOAMD64 print-VERSION print-TIMESTAMP print-COMMITSHA print-FISSION-CLI-SUFFIX print-SKAFFOLD_PROFILE

### Static checks
check: test-run build-fission-cli clean

code-checks: verify-gomod
	golangci-lint run

# Fail if go.mod does not keep direct and indirect requirements in separate
# blocks. `go mod tidy` does not enforce this layout, so this guard does.
# Convention: .claude/resources/go-mod-conventions.md
.PHONY: verify-gomod
verify-gomod:
	@hack/verify-gomod.sh

### License headers
# Files that should carry an SPDX license header. Shared by the license
# targets and the CI check so they never drift. The single Makefile is
# excluded (addlicense cannot process extensionless files) and is guarded
# by the grep check in license-check instead.
LICENSE_HOLDER := The Fission Authors
LICENSE_TMPL := hack/license-header.tmpl
LICENSE_FILES = $(shell find . \
	\( -path './vendor' -o -path './dist' -o -path './node_modules' \
	   -o -path './.git' -o -path './.claude' \
	   -o -path './test/integration/testdata' \) -prune -o \
	-type f \( -name '*.go' -o -name '*.sh' -o -name '*.py' -o -name 'Dockerfile*' \) -print)

.PHONY: license license-check
# Add SPDX license headers to any in-scope file missing one.
license:
	@go tool addlicense -c "$(LICENSE_HOLDER)" -f $(LICENSE_TMPL) $(LICENSE_FILES)

# Fail if any in-scope file is missing a license header.
license-check:
	@go tool addlicense -check -c "$(LICENSE_HOLDER)" -f $(LICENSE_TMPL) $(LICENSE_FILES)
	@grep -q "SPDX-License-Identifier: Apache-2.0" Makefile || \
		{ echo "Makefile is missing its SPDX header"; exit 1; }

# run basic check scripts
test-run: code-checks
	hack/runtests.sh
	@rm -f coverage.txt

### Binaries
build-fission-cli:
	@GOOS=$(GOOS) GOARCH=$(GOARCH) GOAMD64=$(GOAMD64) GORELEASER_CURRENT_TAG=$(VERSION) goreleaser build --snapshot --clean --single-target --id fission-cli

install-fission-cli:
	# goreleaser suffixes the dist dir with the arch microarchitecture level
	# (GOAMD64 for amd64 e.g. _v1, GOARM64 for arm64 e.g. _v8.0, GOARM for arm),
	# which varies per machine -- resolve the dir by glob instead of hardcoding it.
	@dir="$$(ls -d dist/fission-cli_$(GOOS)_$(GOARCH)*/ 2>/dev/null | head -n1)"; \
	if [ -z "$$dir" ]; then \
		echo "install-fission-cli: no dist/fission-cli_$(GOOS)_$(GOARCH)* directory found; run 'make build-fission-cli' first" >&2; \
		exit 1; \
	fi; \
	mv "$$dir/fission$(FISSION-CLI-SUFFIX)" /usr/local/bin/fission

### Codegen
codegen:
	@./hack/update-codegen.sh
	go tool controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."

### CRDs
generate-crds:
	go tool controller-gen crd \
	paths=./pkg/apis/core/v1  \
	output:crd:artifacts:config=crds/v1

### Webhook generation: it generates webhook configs from the +kubebuilder:webhook
### markers in pkg/webhook/.
###
### Output goes to bin/webhooks/ (gitignored), NOT the chart templates
### directory. The chart's hand-maintained webhooks.yaml is the canonical
### Helm template; the generated manifests.yaml is a reference snapshot
### used to keep webhooks.yaml in sync with the markers. After any
### +kubebuilder:webhook change, diff bin/webhooks/manifests.yaml against
### the Mutating/ValidatingWebhookConfiguration sections in
### charts/fission-all/templates/webhook-server/webhooks.yaml and port
### any new/changed rules across.
generate-webhooks:
	go tool controller-gen webhook \
	 paths=./pkg/webhook \
	 output:dir=bin/webhooks


create-crds:
	@kubectl create -k crds/v1

update-crds:
	@kubectl replace -k crds/v1

delete-crds:
	@kubectl delete -k crds/v1

### Cleanup
clean:
	@rm -f dist/

### Misc
generate-swagger-doc:
	@./hack/update-swagger-docs.sh

generate-cli-docs:
	go run tools/cmd-docs/main.go -o "../fission.io/content/en/docs/reference/fission-cli"

generate-crd-ref-docs:
	# crd-ref-docs: https://github.com/elastic/crd-ref-docs
	go tool crd-ref-docs --source-path=pkg/apis/core/v1 --config=tools/crd-ref-docs/config.yaml --renderer markdown
	cp tools/crd-ref-docs/header.md crd_docs.md
	cat out.md >> crd_docs.md && rm out.md
	mv crd_docs.md ../fission.io/content/en/docs/reference/crd-reference.md

all-generators: codegen generate-crds generate-swagger-doc generate-cli-docs generate-crd-ref-docs

skaffold-prebuild:
	@GOOS=linux GOARCH=amd64 GORELEASER_CURRENT_TAG=$(VERSION) goreleaser build --snapshot --clean --single-target
	@cp -v cmd/builder/Dockerfile dist/builder_linux_amd64_v1/Dockerfile
	@cp -v cmd/fetcher/Dockerfile dist/fetcher_linux_amd64_v1/Dockerfile
	@cp -v cmd/fission-bundle/Dockerfile dist/fission-bundle_linux_amd64_v1/Dockerfile
	@cp -v cmd/reporter/Dockerfile dist/reporter_linux_amd64_v1/Dockerfile
	@cp -v cmd/preupgradechecks/Dockerfile dist/pre-upgrade-checks_linux_amd64_v1/Dockerfile
	@find dist/ -name 'Dockerfile' -exec sed -i.bak 's|$$TARGETPLATFORM/||g' {} +; find dist/ -name 'Dockerfile.bak' -delete

skaffold-deploy: skaffold-prebuild
	skaffold run -p $(SKAFFOLD_PROFILE)

### Release
release:
	@./hack/generate-helm-manifest.sh $(VERSION)
	@./hack/release.sh $(VERSION)
	@./hack/release-tag.sh $(VERSION)
	@./hack/changelog.sh
